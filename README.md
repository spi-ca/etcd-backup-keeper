# etcd-backup-keeper

etcd의 온라인 스냅샷을 로컬에 생성하고 무결성을 검사하며 보관 개수를 관리하는
Linux용 Go CLI입니다. 자체 데몬 대신 외부 스케줄러로 실행합니다.

저장소: <https://github.com/spi-ca/etcd-backup-keeper>

## 지원 범위와 한계

- etcd **3.7** 계열을 지원하며 개발·격리 검증 기준은 **3.7.3**입니다.
- Linux와 `/proc`, 동일 실행 파일을 다시 실행하는 로컬 검사 helper가 필요합니다.
  런타임에 외부 `etcdctl`/`etcdutl`은 필요하지 않습니다.
- 최소 Go **1.26**, 개발·확장 검증 계열은 **1.27**입니다.
- 패키지 검증 기준은 **Ubuntu 24.04 amd64**입니다. 실제 배포 OS/CPU와 운영
  볼륨·systemd 환경의 적합성은 별도로 확인해야 합니다.
- 운영 중인 한 멤버에서 전체 스냅샷을 받습니다. 서비스 중지·재시작, 자동
  defrag/compaction, 클러스터 구성 변경은 하지 않지만 디스크·네트워크 부하는 발생합니다.
- 자동 복원, 원격 업로드, 증분 백업, Kubernetes 컨트롤러, 자체 스케줄러는 범위 밖입니다.

## 빌드와 첫 실행

Linux 개발 환경에서 프로젝트 루트를 기준으로 실행합니다. 의존성 준비에는
네트워크가 필요할 수 있습니다. 아래는 버전 주입 없는 기본 빌드입니다.

```sh
mkdir -p bin
CGO_ENABLED=0 go build -trimpath -o bin/etcd-backup-keeper ./cmd/etcd-backup-keeper
bin/etcd-backup-keeper version
bin/etcd-backup-keeper --help
```

[설정 예시](config.example.toml)를 private 설정 파일로 복사한 뒤 endpoint,
인증서 경로, 백업 디렉터리를 수정합니다. 평문 비밀번호는 설정이나 인자에 넣지
않습니다. 권한·TLS·저장 공간 준비는 [운영 안내](docs/operations.md)를 따릅니다.
기본 설정 경로는 `/etc/etcd-backup-keeper/config.toml`입니다.

```sh
etcd-backup-keeper backup --config /etc/etcd-backup-keeper/config.toml
etcd-backup-keeper status --config /etc/etcd-backup-keeper/config.toml
etcd-backup-keeper verify --config /etc/etcd-backup-keeper/config.toml
```

소스 빌드만 한 경우 `etcd-backup-keeper` 대신 `bin/etcd-backup-keeper`를 사용합니다.
처음에는 승인된 격리 etcd에서 백업·검증·[복원 훈련](docs/restore-drill.md)을 수행하고,
그 결과와 실제 운영 조건을 확인한 뒤 스케줄러를 활성화합니다.

## 명령

```text
etcd-backup-keeper backup [--config PATH] [--json] [--verbose]
etcd-backup-keeper status [--config PATH] [--json]
etcd-backup-keeper verify [--config PATH] [--json] [--backup NAME]
etcd-backup-keeper version [--json]
```

| 명령 | 의미 |
|---|---|
| `backup` | 후보 신원·공간 점검, 스냅샷 검사·확정 후에만 보관 정리 |
| `status` | 오프라인 목록·메타데이터·크기 조회. 스냅샷 전체 해싱 없음 |
| `verify` | 기본은 **모든 완료 백업** 검사. `--backup NAME`은 정확한 완료 디렉터리 이름 하나 |
| `version` | 설정 로드나 etcd 접속 없이 버전 출력 |

`status`/`verify`는 etcd에 접속하지 않고 인증서·개인키·비밀번호 파일도 읽지
않습니다. 유효한 설정은 필요합니다. 완료 백업이 없으면 `verify`는 실패합니다.
`--json`은 자동화용이며, `backup --verbose`의 단계 진단은 stderr로 출력합니다.

## 저장과 검증의 의미

```text
backup_dir/
  .lock
  inprogress-<UTC>-<random>/  # 미완료: 자동 삭제하지 않음
  backup-<UTC>-<random>/     # 완료
    snapshot.db
    metadata.json
    SHA256SUMS
  deleting-<UTC>-<random>/   # 중단된 보관 정리: 일부 파일만 남을 수 있음
```

- 디렉터리 `0700`, 파일 `0600`, 실행 UID 소유를 요구합니다. 저장 경로의 symlink를
  따르지 않으며 상위 디렉터리는 운영자가 통제해야 합니다.
- `backup`은 배타 잠금, 로컬 조회·검증은 공유 잠금을 사용합니다. 충돌하면 기다리지
  않고 실패합니다. `.lock`을 지워 잠금을 우회하지 마세요.
- 파일 → staging 디렉터리 sync → 덮어쓰기 없는 원자적 rename → 백업 루트 sync
  이후에만 오래된 완료 백업을 정리합니다. 새 백업은 시계 역행 시에도 보존합니다.
- 확정 전 실패는 이전 완료 백업과 미완료 staging을 보존합니다. 확정 후 sync나
  정리 실패는 **백업이 존재해도 비정상 종료**할 수 있습니다. 먼저 `status`를 확인합니다.
- 검증은 내장 체크섬, 자체 SHA-256, manifest·메타데이터 및 공식 bbolt 구조 검사를
  포함합니다. 신뢰된 출처·암호학적 클러스터 신원·복원 성공을 보장하지 않습니다.

etcd 3.7.3의 Snapshot 응답은 `Header`를 채우지 않습니다. 후보 및 선택 멤버의
스냅샷 전·후 Status를 확인하고 응답 Header가 제공되면 추가로 검사하지만,
스트림 자체의 신원을 증명하지는 않습니다. TLS와 endpoint를 신뢰해야 합니다.
첫 실행에 도달 불가한 후보의 신원은 알 수 없습니다.

## 배포와 문서

`.deb` 빌더는 **명시적으로 제공한 정적 Linux amd64 바이너리·버전·고정 문서
입력·maintainer**를 사용합니다. Go 빌드나 apt 설치를 자동 수행하지 않습니다.
설치·업그레이드는 백업을 실행하거나 unit을 자동 활성화하지 않습니다.

- [운영 안내](docs/operations.md): 설정, TLS/인증, 권한, 스케줄러, 실패·취소 대응
- [격리 복원 훈련](docs/restore-drill.md): 새 데이터 디렉터리·포트에서 수동 확인
- [Ubuntu 패키징](packaging/README.md): 명시적 빌드 입력과 설치·업그레이드·제거
- [통합 테스트](integration/README.md): 격리 etcd, TLS/인증, 복원과 실패 시나리오
- [CI 범위](.github/README.md): Go 버전별 검사와 증거 수집의 한계
- [개발 기준과 요구사항 추적](docs/requirements.md): 완료 증거와 설계 근거

## 개발 검증 상태

로컬 단위 테스트·race·vet·정적 빌드와, 실제 CLI의 백업 → 공식 도구 복원 → 값
읽기 스모크가 확인되었습니다. `status`/`verify`/retention도 구현되었습니다.
최종 바이너리의 격리 제품 통합 18개 시나리오(TLS/mTLS·비밀번호 인증·복원·
후보 전환·클러스터 불일치·보관 정리·변조·실패 보존·SIGTERM 취소·오프라인 조회)가
모두 통과했고 정리 완료도 확인되었습니다. 최종 실제 바이너리와 운영·복원 문서를
포함한 격리 dpkg 수명주기·설정/백업 보존·재현 빌드·정적 unit 검사도 통과했습니다.
Hosted CI 상태와 실행별 증거는 [Actions](https://github.com/spi-ca/etcd-backup-keeper/actions)에서
확인하세요. 로컬 검증만으로 hosted 실행 성공을 추정하지 않습니다.

rootless 환경의 live systemd 시험은 namespace 설정 권한 제한으로 실패했습니다.
정적 unit 검사나 offline 패키지 시험을 실제 서비스·timer 실행 증거로 보지 않습니다.
운영 배포 승인이나 복원 보증을 의미하지 않습니다.

## 라이선스

BSD-3-Clause, Copyright © 2026 Sangbum Kim. 전문은 [LICENSE](LICENSE)에 있습니다.
실제 제품 의존성과 Go 표준 라이브러리의 고지는 `scripts/third-party-notices.sh`로
생성하는 [THIRD_PARTY_NOTICES](THIRD_PARTY_NOTICES)에 기록합니다.
