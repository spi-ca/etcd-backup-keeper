# etcd-backup-keeper

etcd의 온라인 전체 스냅샷을 로컬에 저장하고, 무결성을 검사하며 보관 개수를
관리하는 Linux용 Go CLI입니다. 자체 데몬 없이 수동 실행하거나 외부 스케줄러로
실행합니다. 런타임에 외부 `etcdctl`/`etcdutl`은 필요하지 않습니다.

저장소: <https://github.com/spi-ca/etcd-backup-keeper>

## 지원 범위

- etcd **3.7** 계열을 지원하며 개발·격리 검증 기준은 **3.7.3**입니다.
- Linux와 `/proc`, 동일 실행 파일을 다시 실행하는 로컬 검사 helper가 필요합니다.
- 소스 빌드에는 최소 Go **1.26**이 필요합니다.
- 패키지 검증 기준은 **Ubuntu 24.04 amd64**입니다. 실제 배포 OS/CPU, 운영
  볼륨의 내구성, systemd 환경은 배포 대상에서 별도로 확인해야 합니다.
- 한 멤버에서 스냅샷을 받으며 디스크·네트워크 부하가 발생합니다. 서비스 중지·
  재시작, defrag/compaction, 클러스터 구성 변경은 하지 않습니다.
- 자동 복원, 원격 업로드, 증분 백업, 자체 스케줄러는 제공하지 않습니다.

## 안전한 첫 실행

### 1. 소스 빌드와 도움말 확인

Linux에서 프로젝트 루트를 기준으로 실행합니다. 의존성 준비에는 네트워크가
필요할 수 있습니다. 아래 기본 빌드에는 릴리스 버전을 주입하지 않습니다.

```sh
mkdir -p bin
CGO_ENABLED=0 go build -trimpath -o bin/etcd-backup-keeper ./cmd/etcd-backup-keeper
bin/etcd-backup-keeper version
bin/etcd-backup-keeper --help
```

### 2. 첫 백업 전에 설정·인증·저장소 준비

[설정 예시](config.example.toml)를 private 설정 파일로 **복사하고 검토·수정한 뒤**
실행하세요. 기본 경로는 `/etc/etcd-backup-keeper/config.toml`입니다.

- endpoint가 의도한 클러스터인지 확인하고 TLS 인증서·키와 필요한 인증을 준비합니다.
  평문 비밀번호를 설정이나 인자에 넣지 말고 `password_file`로 별도 배치합니다.
- 백업 경로, 보관 개수, 여유 공간과 실행 UID를 확인합니다. 백업 디렉터리는
  `0700`, 파일은 `0600`이며 실행 UID가 소유해야 합니다.
- 구체적인 준비·권한·TLS 절차는 [운영 안내](docs/operations.md)를 따릅니다.
  스냅샷에는 비밀 정보가 포함될 수 있으므로 저장소 접근을 제한하세요.

선택적으로 `bin/etcd-backup-keeper check --config /etc/etcd-backup-keeper/config.toml`로
설정·명시한 인증 자료·저장 경로를 **읽기 전용·오프라인** 점검할 수 있습니다.
`--connect`를 명시해야 후보별 Status에 접속하며 스냅샷은 요청하지 않습니다.
`unverified`는 미확인 항목이고 종료 `0`도 쓰기·내구성·백업/복원 성공 보증이 아닙니다.

### 3. 승인된 격리 etcd에서 백업·검증

다음은 위 설정 준비가 끝난 뒤 실행하는 예시입니다. 운영 클러스터에서 바로
시작하지 말고, 먼저 격리 환경에서 백업과 [복원 훈련](docs/restore-drill.md)을 확인하세요.

```sh
bin/etcd-backup-keeper backup --config /etc/etcd-backup-keeper/config.toml
bin/etcd-backup-keeper status --config /etc/etcd-backup-keeper/config.toml
bin/etcd-backup-keeper verify --config /etc/etcd-backup-keeper/config.toml
```

## 명령

| 명령 | 용도와 주요 옵션 |
|---|---|
| `backup` | 스냅샷 검사·확정 후에만 보관 정리. `--verbose`로 stderr 진단 출력 |
| `status` | 오프라인 목록·메타데이터·크기 조회. 스냅샷 전체 해싱은 하지 않음 |
| `verify` | 기본은 **모든 완료 백업** 검사. `--backup NAME`으로 정확한 완료 디렉터리 이름 하나 선택 |
| `check` | 설정·인증 자료·저장 경로의 제한적 읽기 전용 점검. 기본 오프라인, `--connect`는 후보별 Status만 요청 |
| `version` | 설정 로드나 etcd 접속 없이 버전 출력 |

`COMMAND --help`로 명령별 옵션·기본값·예시를 확인합니다(예: `verify --help`).
`--config PATH`로 설정을 지정하고 `--json`으로 자동화용 출력을 받습니다.
`status`/`verify`도 유효한 설정은 필요하지만 etcd에 접속하거나 인증서·개인키·
비밀번호 파일을 읽지 않습니다. 완료 백업이 없으면 `verify`는 실패합니다.

## 결과를 해석할 때

- **무결성 검사 성공은 복원 성공을 보장하지 않습니다.** 출처의 진위나 애플리케이션
  정합성도 보증하지 않으므로 신뢰할 수 있는 TLS·endpoint와 별도 복원 훈련이 필요합니다.
- `backup` 성공은 새 백업 확정과 보관 정리 완료를 뜻합니다. 단, 확정 후 sync·정리
  실패나 취소는 **새 백업이 있어도 비정상 종료**할 수 있습니다. 즉시 재실행하지 말고
  `status`와 [실패 대응 절차](docs/operations.md)를 먼저 확인하세요.
- 잠금 충돌이나 미완료 항목은 강제 삭제로 해결하지 마세요. 저장 구조·잠금·확정
  절차와 수동 점검 방법도 운영 안내가 기준입니다.

## 다음 문서

패키지의 systemd timer는 **운영자가 명시적으로 활성화**합니다. 설치·업그레이드는
unit을 자동 활성화·시작하거나 백업을 실행하지 않습니다. 수동 백업·검증·복원 훈련과
실제 배포 조건을 확인한 뒤 스케줄러를 준비하세요.

- [운영 안내](docs/operations.md): 설정·인증, 저장소, 출력·종료 코드, 실패 대응, 스케줄러
- [격리 복원 훈련](docs/restore-drill.md): 새 데이터 디렉터리·포트에서 수동 복원 확인
- [Ubuntu 패키징](packaging/README.md): 명시적 빌드 입력, 설치·업그레이드·제거
- [통합 테스트](integration/README.md): 격리 etcd의 TLS·인증·복원·실패 시나리오
- [요구사항과 검증 매핑](docs/requirements.md): 설계 근거, 로컬 검증 기록과 한계
- [CI 범위](.github/README.md) · [Actions](https://github.com/spi-ca/etcd-backup-keeper/actions): 검사 범위와 hosted 실행 결과

로컬 검증 기록이나 CI 성공은 운영 배포 승인·복원 보증이 아닙니다. 특히 offline
패키지·정적 unit 검사는 실제 service/timer 실행 검증을 대신하지 않습니다.

## 라이선스

BSD-3-Clause, Copyright © 2026 Sangbum Kim. [LICENSE](LICENSE)와 의존성·Go 표준
라이브러리 고지인 [THIRD_PARTY_NOTICES](THIRD_PARTY_NOTICES)를 참고하세요.
