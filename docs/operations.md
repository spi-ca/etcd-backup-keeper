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

안정판 etcd **3.6.x / 3.7.x** 서버와 스냅샷 저장 형식 **3.6.0 / 3.7.0**만
허용합니다. patch 번호는 서버 버전에만 적용하며 prerelease, 알 수 없는 버전이나
다른 minor/major는 거부합니다. 롤링 업그레이드 중 서버와 저장 형식의 minor가
달라도 각각 지원 범위이면 허용하지만, 스트림의 저장 형식과 실제 로컬 검사 결과는
일치해야 합니다. 버전 검사를 생략하는 옵션은 없습니다.

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

- 사람용 `status`는 `completed`·`inprogress`·`deleting` 개수와 완료 목록을
  표시합니다. `Completed snapshot bytes`는 **완료 스냅샷 파일 크기의 합계**이며
  IEC 단위(`KiB`, `MiB` 등)와 정확한 바이트 수를 함께 표시합니다. 메타데이터·
  미완료·삭제 대기 파일을 포함한 전체 볼륨 사용량이나 여유 공간이 아닙니다.
- `Latest completed`는 저장된 완료 시각 기준의 백업 이름·UTC 시각·경과 시간입니다.
  미래 시각이면 음수나 `0s ago` 대신 `future timestamp; check clock skew`로
  표시합니다. 최근 실행 시도·마지막 verify 결과·백업 노후화의 건강 판정은 아닙니다.
  완료 항목별로 이름·revision·스냅샷 크기·UTC 완료 시각도 표시합니다.
- 미완료·삭제 대기가 있으면 `ATTENTION`과 상태별 이름을 표시합니다. 항목을
  임의로 삭제하거나 완료로 간주하지 말고 아래 실패 대응 절차를 따르세요.
  **전체 스냅샷을 해싱하지 않으므로 status 성공만으로 손상이 없다고 판단하지 마세요.**
  빈 저장소에서도 `Integrity not checked by status; run verify.`를 표시합니다.
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

### 선택적 읽기 전용 점검: check

```sh
etcd-backup-keeper check --config /etc/etcd-backup-keeper/config.toml
# 원격 접속을 의도한 경우에만 명시적으로 선택:
etcd-backup-keeper check --config /etc/etcd-backup-keeper/config.toml --connect --json
```

`check`는 기본 **완전 오프라인**입니다. 일반 명령과 같은 strict 설정 검증 후,
`status`/`verify`와 달리 명시한 TLS·비밀번호 자료를 기존 크기·파일 안전성 제한으로
읽어 client 설정만 구성합니다. client 생성·DNS 조회·접속은 하지 않으며 시스템 CA
등록이나 인증 자료 생성도 하지 않습니다. HTTPS의 실제 서버 신뢰·인증 성공은 별개입니다.

저장소는 경로별 no-symlink 디렉터리 descriptor와 권한만 읽습니다. 기존 루트는 실행 UID
소유의 정확한 `0700`이어야 하며 `.lock` 없는 private 빈 루트도 허용합니다. 상위 경로는
root 또는 실행 UID 소유이고 group/world 쓰기가 없어야 합니다(root 소유 sticky 경로는
예외). 상위 경로가 준비된 마지막 루트 부재는 `unverified`이며 생성하지 않습니다.
상위 경로 부재·접근 불가·안전하지 않은 소유권/권한·symlink는 실패입니다. 디렉터리
내용을 열거하거나 임의 트리를 걷지 않고, `.lock` 생성/교체/획득, 쓰기·sync·chmod·자동
수정도 하지 않습니다. **제한된 경로/권한 검사이지 전체 백업 preflight나 쓰기 보증이 아닙니다.**

`--connect`만 원격 client 생성·인증과 **각 후보의 Status**를 허용합니다. 전체는
`request_timeout`, 후보별 생성 중 동기 인증과 Status는 남은 `dial_timeout`으로 제한합니다.
유효한 header·0이 아닌 cluster/member ID·안정판 etcd 3.6.x/3.7.x 버전, `expected_cluster_id` 및 관측한
도달 가능 후보 사이의 cluster 일치를 검사합니다. 후보 하나라도 접속/Status 실패하면
전체가 실패하며 다른 후보 성공으로 숨기지 않습니다. 스냅샷은 요청하지 않으므로 Status
성공도 스냅샷 권한을 확인한 것은 아닙니다. 이전 완료 백업의 cluster pin은 읽지 않습니다.

각 행은 `confirmed`(명시된 제한 안에서 확인), `unverified`(미확인), `failed`(관측된 실패)입니다.
JSON은 기존 envelope의 새 `check` data에 `connect`·`rows`를 담고 실패 때도 가능한 행을
보존합니다. 주소·설정 경로·비밀번호·원본 오류 대신 정적 문구와 후보 번호만 표시합니다.
종료 `0`은 **관측된 실패 없음**일 뿐 전체 준비 완료가 아닙니다. 쓰기 가능성·잠금·fsync·
덮어쓰기 없는 확정·내구성·스냅샷 권한·기존 백업 pin·스냅샷 무결성·복원 가능성은 항상
미확인으로 남습니다. 이후 승인된 격리 환경의 실제 백업·verify·복원 훈련을 대체하지 않습니다.

### 명령별 도움말

`etcd-backup-keeper --help`는 전체 명령 목록을, `COMMAND --help` 또는
`COMMAND -h`는 해당 명령의 정확한 옵션·기본값·짧은 예시·출력/종료 의미를
표시합니다. 예를 들어 `verify --help`는 기본 대상이 **모든 완료 백업**임을
명시합니다. `--config`·`--json`은 명령 앞뒤에서 사용할 수 있으며 `--verbose`는
`backup`, `--backup NAME`은 `verify`, `--connect`는 `check`에서만 지원합니다.

도움말과 `version`은 설정을 로드하거나 etcd에 접속하지 않습니다. `version`도
공통 `--config PATH`를 받지만 해당 파일을 읽지 않습니다. `COMMAND --help --json`은
기존 JSON envelope의 `data.usage` 문자열로 해당 명령의 도움말을 제공합니다.
잘못된 옵션은 도움말과 함께 지정해도 사용법 오류이며 입력 값을 진단에 노출하지 않습니다.

### 출력과 종료 코드

사람용 status의 단위·경과 시간·주의 표시는 JSON 필드나 값에 추가되지 않습니다.
`--json`은 stdout에 단일 object를 출력합니다. `command`, `ok`, 성공 시 `data`,
실패 시 `error.code`·`error.message`를 사용합니다. 확정 후 실패는 `ok: false`여도
`data`에 새 백업 정보와 정리 대기 상태가 있을 수 있습니다. 종료 코드와 `ok`를 함께
확인하세요. `backup --verbose`는 stderr에 후보 번호/전체 수(주소 제외), 실제 시도·전환,
수신 누적 바이트, 체크섬·검사, 확정, 루트 동기화, 보존 정리 단계를 표시합니다.
체크섬·검사·확정·동기화·정리 표시는 해당 작업의 시작을 뜻하며,
`stage=complete`는 확정·동기화·정리가 모두 성공한
경우에만 나옵니다. 수신량은 시도마다 초기화하고 약 1초 간격으로 갱신하며,
작거나 실패한 스트림도 마지막 수신량(`final=true`)을 표시합니다. 이 표시는
수신 종료 시점의 바이트 수이지 검증·확정 성공을 뜻하지 않습니다. 경과 시간은
전체 작업 기준이며 예상 완료 시간·진행률은 제공하지 않습니다. 주소·인증 정보·원본
오류는 진단에 포함하지 않으며 JSON stdout 형식은 바뀌지 않습니다.
진단 출력 실패로 확정·정리를 중단하지 않습니다. 작업이 성공해도 출력 실패 시
종료 코드는 `1`일 수 있으며, 작업 자체도 실패하면 작업 오류 코드가 우선합니다.

| 종료 코드 | 의미 |
|---|---|
| `0` | 명령 성공 |
| `1` | 일반 작업·저장·검증·정리 실패 또는 출력 실패 |
| `2` | 사용법·설정·인증 자료·TLS 설정 오류 |
| `124` | 작업 timeout |
| `130` | SIGINT/SIGTERM 등으로 취소 |

오류 메시지는 비밀이나 DB key/value가 노출되지 않도록 일반화됩니다. 사람이 읽는
stderr에는 알려진 오류 코드별 정적 `hint:`가 추가됩니다. 설정·인증·잠금·공간·검증·
원격 연결·취소/timeout 등에 대한 점검 안내이며 원본 오류나 설정 값은 포함하지 않습니다.
JSON의 `data`·`error.code`·`error.message` 및 기존 종료 코드 계약은 그대로 유지합니다.
자동화는 문자열 전체보다 종료 코드와 JSON 오류 코드를 기준으로 처리하세요.

확정 후 실패의 첫 안내는 `durability_uncertain`이면 백업 이름은 존재하지만 루트 내구성이
불확실하고 보관 정리를 실행하지 않았음을, `retention_incomplete`이면 백업 확정 후 정리가
끝나지 않았음을 구분합니다. 확정 후 취소/timeout은 중단 단계를 추정하지 않고 작업 미완료와
`status` 점검만 안내합니다. `status` 성공은 내구성·무결성·복원 성공의 보증이 아닙니다.
원인과 남은 상태를 확인하기 전에 무조건 재실행하거나 `.lock`·미완료·삭제 대기 항목을
임의로 지우지 마세요.

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
