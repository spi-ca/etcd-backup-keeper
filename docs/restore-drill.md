# 격리 복원 훈련

백업 검증 성공과 복원 성공은 다릅니다. 이 절차는 **운영 클러스터와 분리된
일회성 환경**에서 공식 도구로 복원한 뒤 데이터를 읽는 수동 훈련입니다.
제품은 자동 복원이나 운영 클러스터 변경을 수행하지 않습니다.

재현 가능한 제품 통합 시나리오는 [통합 테스트](../integration/README.md),
백업 선택·검증과 권한은 [운영 안내](operations.md)를 참고하세요.

## 안전 조건

- 처음에는 비밀이 없는 자체 fixture 데이터를 사용하세요. 실제 스냅샷을 쓸 때는
  훈련 호스트·복사본·출력에도 운영 데이터와 같은 접근 통제를 적용합니다.
- **새 데이터 디렉터리, 별도 client/peer 포트, 새 클러스터 token·멤버 구성**만
  사용합니다. 운영 data-dir을 덮어쓰거나 기존 멤버에 연결하지 않습니다.
- 운영 endpoint·kubeconfig·운영 프로세스를 테스트에 사용하지 않습니다. 예시의
  loopback과 포트도 격리 호스트에서 사용 중이 아닌지 먼저 확인합니다.
- 개발용 외부 도구 `etcd`, `etcdctl`, `etcdutl` **3.7.3**을 별도로 준비하고 버전을
  확인합니다. 제품 CLI의 런타임 의존성이 아니며 제품이 설치·다운로드하지 않습니다.
- 공식 절차: <https://etcd.io/docs/v3.7/op-guide/recovery/>. 운영 재해 복구는 실제
  etcd/Kubernetes 버전·토폴로지에 맞는 별도 승인된 계획이 필요합니다.

## 1. 완료 백업 선택과 오프라인 검사

원본 백업을 바꾸지 않는 상태에서 진행합니다. 보관 정리와 경합하지 않도록
스케줄러와 backup 실행을 멈추고 진행 중인 작업이 끝났는지 확인하세요.

```sh
etcd-backup-keeper status --config /etc/etcd-backup-keeper/config.toml
# 실제 status에서 선택한 정확한 완료 디렉터리 이름으로 바꿉니다.
BACKUP_NAME='backup-실제이름'
etcd-backup-keeper verify --config /etc/etcd-backup-keeper/config.toml \
  --backup "$BACKUP_NAME"
```

`inprogress-*`나 `deleting-*`는 사용하지 않습니다. `status`는 전체 해시 검사가
아니므로 이름 지정 `verify`도 반드시 수행합니다. 다음 예시는 기본 백업 루트를
가정합니다. 다른 설정이면 실제 경로로 바꾸세요. private 훈련 작업 디렉터리는
운영 데이터·백업 루트 밖의 승인된 위치에서 만듭니다.

```sh
umask 077
DRILL=$(mktemp -d "$PWD/restore-drill.XXXXXXXX")
SOURCE="/var/lib/etcd-backup-keeper/$BACKUP_NAME"
cp -- "$SOURCE/snapshot.db" "$SOURCE/metadata.json" "$SOURCE/SHA256SUMS" "$DRILL/"
chmod 600 "$DRILL/snapshot.db" "$DRILL/metadata.json" "$DRILL/SHA256SUMS"
(cd "$DRILL" && sha256sum --check SHA256SUMS)
etcdutl snapshot status "$DRILL/snapshot.db" --write-out=table
```

복사본의 SHA-256을 다시 확인하고, 원본의 `metadata.json`과 revision·키 수·크기를
대조합니다. 이어지는 공식 restore에서도 내장 snapshot 체크섬 검증을 유지합니다.
오류가 나면 중단하며 **`--skip-hash-check`로 우회하거나 manifest를 재생성하지
않습니다.** 해시 일치 자체는 출처의 진위나 복원 성공을 보증하지 않습니다.

## 2. 새 클러스터로 복원

다음 예시는 **격리 호스트의 단일 멤버 훈련**입니다. 운영 토폴로지에 적용하는
설정이 아닙니다. `$DRILL/data`는 아직 존재하지 않아야 하며, 아래의 client
`32379`와 peer `32380`은 운영 포트와 겹치지 않는 미사용 포트여야 합니다.

```sh
TOKEN="restore-drill-$(basename "$DRILL")"
etcdutl snapshot restore "$DRILL/snapshot.db" \
  --data-dir "$DRILL/data" \
  --name drill \
  --initial-cluster 'drill=http://127.0.0.1:32380' \
  --initial-advertise-peer-urls 'http://127.0.0.1:32380' \
  --initial-cluster-token "$TOKEN"
```

공식 restore는 새 클러스터·멤버 신원으로 데이터를 구성합니다. 새 token만으로
운영 망 분리가 되는 것은 아닙니다. 네트워크와 데이터 경로를 별도로 격리하세요.
아래 HTTP는 격리된 loopback 훈련 전용이며 운영 TLS 구성의 대체가 아닙니다.

## 3. 별도 인스턴스에서 데이터 읽기

같은 shell에서 격리 인스턴스를 실행합니다. 로그에도 데이터가 포함될 수 있으므로
작업 디렉터리는 private 상태로 유지합니다.

```sh
etcd --name drill --data-dir "$DRILL/data" \
  --listen-client-urls 'http://127.0.0.1:32379' \
  --advertise-client-urls 'http://127.0.0.1:32379' \
  --listen-peer-urls 'http://127.0.0.1:32380' \
  --initial-advertise-peer-urls 'http://127.0.0.1:32380' \
  --initial-cluster 'drill=http://127.0.0.1:32380' \
  --initial-cluster-token "$TOKEN" >"$DRILL/etcd.log" 2>&1 &
DRILL_PID=$!
etcdctl --endpoints='http://127.0.0.1:32379' \
  --dial-timeout=2s --command-timeout=5s endpoint health
# 미리 fixture에 기록한 키와 기대 값으로 바꿉니다.
etcdctl --endpoints='http://127.0.0.1:32379' \
  --dial-timeout=2s --command-timeout=5s get 'drill/key' --print-value-only
```

기동 직후 health 실패는 준비 중일 수 있습니다. 프로세스·로그를 확인하고 제한된
시간 안에서만 재시도합니다. 데이터 읽기도 기동 완료 후 진행하세요. 복원 전
기록한 key/value·revision과 비교하고 애플리케이션별 불변 조건을 확인합니다.
일부 키 읽기 성공만으로 모든 운영 데이터의 복구를 보장하지 않습니다.

스냅샷에 etcd auth가 활성화되어 있다면 복원본의 인증 정책도 고려해야 합니다.
위 단순 읽기 예시는 인증 없는 fixture용입니다. 승인된 격리 인증 절차를 따르고,
통과시키려고 운영 인증을 끄거나 비밀번호를 인자에 넣지 마세요.

완료 후 이 shell이 시작한 **정확한 프로세스만** 종료하고 종료 완료를 확인합니다.

```sh
kill -TERM "$DRILL_PID"
wait "$DRILL_PID"
```

기동이 실패했거나 PID의 소유 관계가 불확실하면 먼저 조사합니다. 로그·비교 결과를
필요한 기간 보존하고, 프로세스 종료 후 이 훈련이 만든 정확한 디렉터리만 정리합니다.
원본 백업·운영 data-dir은 삭제하지 않습니다.

## Kubernetes와 revision bump

오래된 스냅샷으로 복원하면 revision이 과거로 돌아갑니다. Kubernetes informer 등
watch 기반 client가 이미 더 높은 revision을 관찰했다면 기존 cache와 watch 상태가
복원 데이터와 어긋날 수 있습니다. 공식 도구의 `--bump-revision`은 revision을
앞으로 늘리고, `--mark-compacted`는 과거 revision의 watch에 compaction을 알려
client가 다시 목록을 읽도록 유도합니다.

별도의 **새** 훈련 data-dir에 적용하는 문법 예시입니다. 아래 증가량은 설명용이며
운영 권장값이 아닙니다. 실제 값은 스냅샷 이후의 revision 증가율·경과 시간·여유를
계산하고 overflow와 버전별 제약을 검토해 정해야 합니다.

```sh
etcdutl snapshot restore "$DRILL/snapshot.db" \
  --data-dir "$DRILL/kubernetes-example-data" \
  --name drill \
  --initial-cluster 'drill=http://127.0.0.1:32380' \
  --initial-advertise-peer-urls 'http://127.0.0.1:32380' \
  --initial-cluster-token "$TOKEN-kubernetes-example" \
  --bump-revision 1000000000 --mark-compacted
```

이 예시는 Kubernetes 복구 절차 전체가 아닙니다. 실제 control-plane 중지·재기동,
모든 멤버의 새 구성, 인증 자료, API server 연결·cache 재동기화는 공식 문서와
승인된 운영 복구 계획에서 다룹니다. 훈련 중 운영 Kubernetes를 연결하지 마세요.

## 남길 검증 결과

도구 버전, 제품 바이너리 해시, 백업 이름·해시·revision, 로컬 verify 결과, 공식
restore 결과, 격리 인스턴스의 읽기 비교와 종료·정리 결과를 연결해 기록합니다.
비밀번호·개인키·스냅샷·민감한 key/value는 공개 로그나 CI artifact에 넣지 않습니다.
실행하지 않은 단계는 미실행으로 표시하고 복원 성공을 추정하지 마세요.
