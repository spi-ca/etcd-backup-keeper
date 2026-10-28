# etcd-backup-keeper

etcd의 온라인 전체 스냅샷을 로컬에 저장하고, 무결성을 검사하며 보관 개수를
관리하는 Linux CLI입니다. 수동 실행하거나 외부 스케줄러로 실행하며, 런타임에
Go나 외부 `etcdctl`/`etcdutl`은 필요하지 않습니다.

## 안전한 첫 실행

1. [Releases](https://github.com/spi-ca/etcd-backup-keeper/releases)에서 배포물과
   `SHA256SUMS`를 받습니다. 현재 공개 릴리스는
   [v20261027-3](https://github.com/spi-ca/etcd-backup-keeper/releases/tag/v20261027-3)입니다.
2. [빠른 시작](docs/quickstart.md)을 따라 **패키지 설치 → 기존 private 설정 편집 →
   오프라인 점검 → 선택적 접속 점검 → 수동 백업·검증**을 진행합니다.
   기본 예시는 kubeadm stacked etcd의 control-plane 노드입니다.
3. [격리 복원 훈련](docs/restore-drill.md)과 실제 배포 조건을 확인한 뒤에만
   timer를 활성화합니다. 패키지 설치·업그레이드는 service/timer를 자동 시작하거나
   활성화하지 않고 백업도 실행하지 않습니다.

배포물은 Ubuntu/Debian amd64 `.deb`, Arch Linux x86_64 `.pkg.tar.zst`,
Linux amd64 정적 바이너리 `.tar.gz`입니다. 패키지 검증 기준은 **Ubuntu 24.04 amd64 /
Arch Linux x86_64**이며 다른 OS·CPU 지원을 뜻하지 않습니다.

## 지원 범위

- 안정판 etcd **3.6.x / 3.7.x**, 스냅샷 저장 형식 **3.6.0 / 3.7.0**만 지원합니다.
  서버와 저장 형식은 독립적으로 검사하며 다른 minor/major, prerelease와 알 수 없는
  버전은 거부합니다. **3.6.8 / 3.7.3** 각각의 격리 백업·검증·복원을 확인했습니다.
- Linux와 `/proc`, 동일 실행 파일을 다시 실행하는 로컬 검사 helper가 필요합니다.
  운영 볼륨의 내구성과 실제 systemd sandbox·timer 동작은 대상 호스트에서 확인해야 합니다.
- 한 멤버에서 스냅샷을 받으므로 디스크·네트워크 부하가 생깁니다. 서비스 중지·재시작,
  defrag/compaction, 클러스터 구성 변경은 하지 않습니다.
- 자동 복원, 원격 업로드, 암호화, 증분 백업, 자체 데몬·스케줄러는 제공하지 않습니다.
  스냅샷에는 비밀 정보가 포함될 수 있으므로 접근 통제와 별도 사본 정책이 필요합니다.

## 결과를 해석할 때

**무결성 검사 성공은 복원 성공·출처의 진위·애플리케이션 정합성 보증이 아닙니다.**
`status`는 전체 해싱도 하지 않습니다. 백업이 보이더라도 확정 후 실패할 수 있으므로
오류 시 무조건 재실행하거나 잠금·미완료 항목을 지우지 마세요.

## 다음 문서

운영자는 아래 순서로 읽습니다.

- [빠른 시작](docs/quickstart.md): 릴리스 설치, kubeadm 인증서, 첫 실행과 timer 활성화
- [운영 안내](docs/operations.md): 설정 계약, 정기 운영, `check`의 미확인 범위,
  JSON·종료 코드, 저장·확정·보관 정리와 업그레이드의 기준 문서
- [문제 해결](docs/troubleshooting.md): 오류 코드별 안전한 조사와 다음 조치
- [격리 복원 훈련](docs/restore-drill.md): 별도 데이터 디렉터리·포트에서 수동 복원 확인

## 명령

| 명령 | 용도 |
|---|---|
| `backup` | 스냅샷 생성·검사·확정 후 보관 정리 |
| `status` | 로컬 백업 목록과 상태 조회 |
| `verify` | 완료 백업의 무결성 검사 |
| `check` | 설정·인증 자료·경로 점검. 원격 접속은 `--connect`로 선택 |
| `version` | 설치된 도구 버전 확인 |

명령별 옵션은 `etcd-backup-keeper COMMAND --help`로 확인합니다. 도움말과
`version`은 설정을 읽거나 etcd에 접속하지 않습니다.

개발자는 [요구사항·소스 빌드·검증 기록](docs/requirements.md),
[패키지 빌드·수명주기 시험](packaging/README.md),
[격리 통합 테스트](integration/README.md), [CI 정의](https://github.com/spi-ca/etcd-backup-keeper/blob/main/.github/workflows/ci.yml)를 참고하세요.
과거 [사용성 개선 계획](https://github.com/spi-ca/etcd-backup-keeper/blob/main/docs/usability-plan.md)은
이력 자료이며 현재 사용법이 아닙니다.
[소스 저장소](https://github.com/spi-ca/etcd-backup-keeper) ·
[Actions](https://github.com/spi-ca/etcd-backup-keeper/actions)

## 라이선스

BSD-3-Clause, Copyright © 2026 Sangbum Kim. [LICENSE](LICENSE)와 의존성·Go 표준
라이브러리 고지인 [THIRD_PARTY_NOTICES](THIRD_PARTY_NOTICES)를 참고하세요.
