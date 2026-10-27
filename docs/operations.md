# 운영 안내

이 문서는 수동 실행과 외부 스케줄러 운영을 설명합니다. 지원 범위와 현재 검증
상태는 [README](../README.md), 배포 입력과 수명주기는
[패키징 안내](../packaging/README.md)를 먼저 확인하세요.

## 1. 실행 환경과 저장소 준비

- Linux와 `/proc`가 필요합니다. 로컬 스냅샷 검사는 동일 실행 파일의 helper를
  실행합니다. 실행 중 바이너리를 교체하거나 sandbox에서 helper 실행을 막지 마세요.
- 실행 UID가 백업 루트와 파일을 소유해야 합니다. 루트·백업 디렉터리는 정확히
  `0700`, `.lock`·백업 파일은 `0600`입니다. symlink·특수 파일·외부 hard link를
  정상 백업 파일로 받아들이지 않습니다.
- `backup_dir`은 정규화된 절대 경로를 사용합니다. 경로의 상위 디렉터리는 미리
  존재해야 하며 운영자가 통제해야 합니다. CLI는 마지막 디렉터리만 생성하고
  상위 경로를 포함해 symlink를 따르지 않습니다.
- 운영 볼륨에서 파일·디렉터리 fsync와 덮어쓰기 없는 원자적 rename의 지원,
  용량·inode·지연을 확인하세요. 개발 환경의 성공을 다른 파일시스템의 내구성
  보장으로 해석하지 마세요.
- 스냅샷에는 비밀 정보가 포함될 수 있습니다. 접근 권한·볼륨 암호화·보관 장소와
  별도 사본 정책은 운영자가 관리합니다. 이 CLI는 암호화나 원격 업로드를 하지 않습니다.

패키지 unit은 기본적으로 root로 실행합니다. kubeadm의 root 전용 인증서를 읽을
수 있도록 한 기본값이며, 일반 CLI가 항상 root를 요구한다는 뜻은 아닙니다.
수동 실행과 timer의 UID·설정·백업 경로를 일치시키세요. 기본 경로는
`/var/lib/etcd-backup-keeper`이며 systemd는 service 실행 시 이를 준비합니다.

## 2. 설정과 인증

[config.example.toml](../config.example.toml)을 복사하고 대상 환경에 맞게 수정합니다.
설정 디렉터리는 운영자 통제하에 두고 파일은 `0600`으로 관리하세요. 인증서·키·비밀번호는
패키지에 넣지 않고 별도 배치합니다. 다음은 비밀 값 없는 HTTPS 설정 예입니다.

```toml
endpoints = ["https://127.0.0.1:2379"]
backup_dir = "/var/lib/etcd-backup-keeper"
keep = 7
min_free_bytes = 1073741824
dial_timeout = "5s"
request_timeout = "30m"
# expected_cluster_id = "0123456789abcdef"
# ca_file = "/etc/etcd-backup-keeper/ca.crt"
# cert_file = "/etc/etcd-backup-keeper/client.crt"
# key_file = "/etc/etcd-backup-keeper/client.key"
# username = "backup-operator"
# password_file = "/etc/etcd-backup-keeper/password"
```

### 설정 계약

평면 TOML이며 **정확한 소문자 키**만 허용합니다. 모르는 키·table은 거부합니다.
허용 키는 위 예시의 12개입니다. 주석 처리된 값은 활성 설정이 아닙니다.

| 키 | 의미 |
|---|---|
| `endpoints` | 순서 있는 후보 URL. 모두 같은 `http` 또는 `https` scheme |
| `expected_cluster_id` | 선택 사항. 0이 아닌 64-bit 16진수, `0x` 접두사 허용 |
| `backup_dir` | private 로컬 백업 루트의 절대 경로 |
| `keep` | 새 완료 백업을 포함한 보관 개수. 1 이상 |
| `min_free_bytes` | 새 스냅샷에 필요한 추정 공간 외에 남길 여유 공간 |
| `dial_timeout` | 후보별 초기 연결·인증·첫 점검 제한. 기본 `5s` |
| `request_timeout` | 설정 로드 후 전체 명령 제한. 기본 `30m`, 모든 후보·스트림·검사·정리 포함 |
| `ca_file` | 선택 PEM CA 경로. 지정 시 시스템 trust 대신 사용 |
| `cert_file`, `key_file` | mTLS용 쌍. 함께 지정 |
| `username`, `password_file` | 사용자 인증용 쌍. 모든 endpoint가 HTTPS여야 함 |

timeout은 양수 Go duration 문자열입니다. 전체 제한은 idle timeout이 아니며
상위 프로세스의 취소가 우선합니다. 여유 공간 점검은 이후 디스크 부족을 완전히
방지하지 못합니다. 미완료 작업과 삭제 대기 항목도 공간을 차지합니다.

### TLS와 비밀번호

- HTTPS는 서버 인증서 체인과 hostname을 검증하며 TLS 1.2 이상을 사용합니다.
  검증 생략 옵션은 없습니다. CA 미지정 시 시스템 trust를 사용합니다.
- URL에 사용자 정보·비밀번호·query·fragment·루트 이외 경로를 넣지 마세요.
  여러 endpoint는 동일한 신뢰·인증 설정으로 접근 가능한 같은 클러스터여야 합니다.
- 인증 관련 파일 경로는 절대 경로입니다. 개인키와 비밀번호 파일은 실행 UID 소유의
  private 일반 파일(`0600` 권장)이어야 하며 마지막 경로의 symlink는 거부합니다.
  상위 디렉터리도 운영자가 통제하세요.
- 비밀번호는 설정 값·프로세스 인자·shell history에 쓰지 말고 승인된 비밀 배치
  방식으로 `password_file`에 넣습니다. 비어 있지 않은 한 줄이며 마지막 LF/CRLF
  하나만 제거하고 공백은 보존합니다. 최대 크기는 64 KiB입니다.
- `status`/`verify`는 설정 형식을 검증하지만 원격 client를 만들거나 인증 자료를
  읽지 않습니다. 원격 etcd나 인증 자료가 없어도 로컬 조회·검증할 수 있습니다.

### 후보와 클러스터 신원

도달 가능한 모든 후보의 Status를 먼저 검사하고, 순서대로 한 멤버에서 스냅샷을
받습니다. 연결·원격 스트림 실패에만 다음 후보를 시도합니다. 로컬 쓰기·검사·확정
실패를 후보 전환으로 숨기지 않습니다. 이전 완료 백업 또는 `expected_cluster_id`와
다른 클러스터는 거부하며 선택 멤버의 전·후 cluster/member ID도 재확인합니다.

etcd 3.7.3의 Snapshot 응답 Header는 비어 있습니다. Header가 제공되면 검사하지만
전·후 Status 확인은 **스트림의 암호학적 신원 증명은 아닙니다.** TLS·endpoint를
신뢰해야 하며 첫 실행에서 접근할 수 없는 후보의 신원은 미확인입니다.

## 3. 실행·조회·검증

```sh
etcd-backup-keeper backup --config /etc/etcd-backup-keeper/config.toml --verbose
etcd-backup-keeper status --config /etc/etcd-backup-keeper/config.toml --json
etcd-backup-keeper verify --config /etc/etcd-backup-keeper/config.toml --json
# NAME은 status에 표시된 정확한 backup-* 디렉터리 이름으로 바꿉니다.
etcd-backup-keeper verify --config /etc/etcd-backup-keeper/config.toml --backup NAME
```

- `status`는 완료·미완료·삭제 대기 목록, 저장된 메타데이터·크기와 마지막 완료
  시각 기준의 성공 백업을 표시합니다. **전체 스냅샷을 해싱하지 않으므로 status
  성공만으로 손상이 없다고 판단하지 마세요.** 영속적인 실행 이력 조회도 아닙니다.
- `verify`의 기본 대상은 최신 하나가 아닌 **모든 완료 백업**입니다. `--backup`에는
  경로가 아닌 완료 백업 이름 하나를 지정합니다. 미완료·삭제 대기는 검증 대상이
  아니며, 완료 백업 부재나 지정 백업 부재는 실패입니다.
- 검증은 내부 trailer 체크섬, 자체 SHA-256, `SHA256SUMS`·메타데이터의 일치,
  공식 bbolt 검사 및 revision·키 수 등의 일치를 확인합니다. 메타데이터와 해시를
  함께 바꾸는 공격자, 출처의 진위, 애플리케이션 수준 정합성, 복원 성공은 보증하지 않습니다.
  실제 복원 확인은 [격리 훈련](restore-drill.md)으로 분리합니다.
- `backup`은 배타 잠금, `status`/`verify`는 공유 잠금을 비차단 방식으로 잡습니다.
  충돌 시 `storage_locked`로 실패하므로 스케줄 간격과 검증 시간을 고려하세요.
  실행 중인 작업이 없다는 확인 없이 `.lock`을 지우거나 교체하지 마세요.

### 출력과 종료 코드

`--json`은 stdout에 단일 object를 출력합니다. `command`, `ok`, 성공 시 `data`,
실패 시 `error.code`·`error.message`를 사용합니다. 확정 후 실패는 `ok: false`여도
`data`에 새 백업 정보와 정리 대기 상태가 있을 수 있습니다. 종료 코드와 `ok`를 함께
확인하세요. `--verbose`의 단계 진단은 stderr이므로 stdout과 섞지 않습니다.

| 종료 코드 | 의미 |
|---|---|
| `0` | 명령 성공 |
| `1` | 일반 작업·저장·검증·정리 실패 또는 출력 실패 |
| `2` | 사용법·설정·인증 자료·TLS 설정 오류 |
| `124` | 작업 timeout |
| `130` | SIGINT/SIGTERM 등으로 취소 |

오류 메시지는 비밀이나 DB key/value가 노출되지 않도록 일반화됩니다. 자동화는
문자열 전체보다 종료 코드와 JSON 오류 코드를 기준으로 처리하세요.

## 4. 확정·보관 정리와 실패 대응

스냅샷 검사와 메타데이터 기록 후 **파일 sync → staging 디렉터리 sync →
덮어쓰기 없는 rename → 백업 루트 sync**를 마쳐야 보관 정리를 시작합니다.
그 전에는 이전 완료 백업을 삭제하지 않습니다. 새 백업은 시계가 역행해도
보관 대상에 고정하고, 나머지 완료 백업을 완료 시각·이름 순으로 정리합니다.

| 상태 | 대응 |
|---|---|
| 확정 전 오류·취소 | 이전 완료 백업과 남은 `inprogress-*`를 보존. 원인·공간·설정 점검 |
| 확정 rename 후 루트 sync 실패 | 완료 이름이 보여도 내구성 불확실. 정리는 하지 않음. 저장장치 오류부터 점검 |
| 확정 후 보관 정리 실패 | 새 백업이 존재해도 비정상 종료. `status`와 필요 시 이름 지정 `verify`로 확인 |
| `deleting-*` 잔존 | 삭제 대기. 다음 **성공적인 새 백업 확정 후** 안전 검사와 정리 재시도 |
| 알 수 없는 파일·symlink·손상된 메타데이터 | 안전하게 실패. 강제 삭제나 해시 재생성으로 덮지 말고 조사 |

보관 정리는 안전 트리를 검사해 `deleting-*`로 격리하고 root sync 후,
`snapshot.db`·`metadata.json`·`SHA256SUMS`만 삭제합니다. 임의 재귀 삭제를 하지
않습니다. `status`나 `verify`가 삭제를 재개하지는 않습니다.

SIGINT/SIGTERM은 취소로 처리합니다. 종료가 완료될 때까지 기다리세요. SIGKILL·
호스트 장애는 정상 취소나 마지막 sync를 보장하지 않습니다. 재시작 전에 상태와
남은 파일을 확인하고, 실패했다고 무조건 같은 백업을 다시 시작하지 마세요.

### 미완료 항목의 수동 점검

`inprogress-*`는 자동 삭제하거나 완료 백업으로 승격하지 않습니다.

1. timer와 기타 실행 주체를 멈추고 모든 backup/verify 프로세스가 끝났는지 확인합니다.
2. 로그·`status`로 정확한 디렉터리를 식별하고 private 권한을 유지해 증거를 보존합니다.
3. 완료 백업을 별도로 `verify`하고 필요한 경우 격리 복원으로 확인합니다.
4. 미완료 항목이 더 이상 필요 없다는 승인 후, 신뢰된 상위 경로에서 symlink·하위
   디렉터리·특수 파일이 없는지 확인해 **확인된 일반 파일과 해당 빈 디렉터리만**
   개별 제거합니다. 예상 밖 항목은 중단·조사합니다.

`rm -rf inprogress-*` 같은 광범위 삭제, `.lock` 삭제, 완료 백업으로의 수동 rename은
하지 마세요. 악의적인 동일 UID·root 프로세스나 privileged mount 조작은 방어 범위 밖입니다.

## 5. systemd 스케줄러와 패키지 수명주기

설치·업그레이드는 unit을 활성화·시작하지 않고 백업도 실행하지 않습니다.
설정·인증·저장소와 수동 백업·검증·복원 훈련을 확인한 후 **운영자가** 활성화합니다.

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now etcd-backup-keeper.timer
systemctl list-timers etcd-backup-keeper.timer
journalctl -u etcd-backup-keeper.service
```

기본 timer는 daily, 최대 15분 무작위 지연, `Persistent=true`입니다. 활성화하면
누락된 실행이 곧 시작될 수 있습니다. service는 oneshot이며 `TimeoutStartSec=45min`입니다.
설정 timeout을 늘리면 unit 제한도 함께 검토하세요.

기본 hardening은 `UMask=0077`, `ProtectSystem=strict`, `ProtectHome=true`, 빈
capability set 등을 포함합니다. 인증 자료를 `/home` 아래에 두지 마세요.
다른 `backup_dir`을 쓰면 소유권·권한과 unit drop-in의 `ReadWritePaths=`도 함께
준비합니다. rootless live systemd 시험은 namespace 설정 단계의 권한 제한으로
실패했으므로 실제 service/timer는 별도 검증이 필요합니다. 이를 통과시키려고
hardening을 무작정 완화하지 마세요.

업그레이드·remove·purge 전에는 운영자가 timer를 멈추고 실행 중 작업의 완료를
기다리거나 명시적으로 취소합니다. 바이너리가 실행 중 교체되지 않도록 하세요.

```sh
sudo systemctl disable --now etcd-backup-keeper.timer
# 완료를 기다리는 대신 취소할 때만:
sudo systemctl stop etcd-backup-keeper.service
# 승인된 패키지 업그레이드/remove/purge 후:
sudo systemctl daemon-reload
```

업그레이드 후 설정·바이너리·unit을 확인하고 의도한 경우에만 timer를 다시
활성화합니다. 자동 stop/reload를 수행하는 maintainer script는 없습니다.
설정은 dpkg conffile이며 로컬 수정 충돌 정책은 운영자가 결정합니다.
remove는 설정을 남기고 purge는 conffile을 지우지만 **둘 다 백업 데이터는
보존**합니다. 운영자가 별도 배치한 키·인증서·비밀번호도 자동 정리하지 않습니다.
