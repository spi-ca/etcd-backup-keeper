# 요구사항과 검증 매핑

현재 사용법은 [README](../README.md), 설정·실패 대응은 [운영 안내](operations.md)가
기준이다. 이 문서는 초기 범위가 구현과 검사에서 누락되지 않았는지 추적한다.

## 확정 범위

- Linux Go CLI, 최소 Go 1.26. etcd 3.7 지원, 실제 격리 검증 기준 3.7.3.
- etcd Go client 직접 사용. 런타임에 외부 etcdctl/etcdutl 실행 파일 불필요.
- 온라인 전체 스냅샷만 생성. 운영 서비스 중지/재시작, defrag/compaction,
  멤버 변경, 자동 복원, 증분 백업, 원격 업로드, 자체 데몬/스케줄러는 제외.
- 순서 있는 복수 endpoint, TLS/mTLS 및 private 파일을 통한 비밀번호 인증.
- control-plane과 별도 백업 노드 모두 설정으로 대응. 실제 노드별 접근 권한은 운영자가 준비.
- Ubuntu 24.04 amd64를 패키지 개발·격리 검증 기준으로 사용. 실제 배포 대상과 볼륨은 별도 확정.
- 공개 저장소는 <https://github.com/spi-ca/etcd-backup-keeper>.
  검증된 MVP와 CI의 첫 커밋·푸시는 사용자 승인 범위다. 운영 배포/릴리스는 별도다.
- 프로젝트는 BSD-3-Clause, 저작권자는 `LICENSE`의 Sangbum Kim.

## 요구사항별 증거

| ID | 요구사항 | 구현·검증 근거 |
|---|---|---|
| CLI | backup/status/verify/version, 엄격 TOML, 사람용/JSON | `internal/cli/*`, `internal/config/*` 및 해당 테스트; 전체 통합 version/backup/verify/offline 시나리오 |
| AUTH | TLS/mTLS, private password_file, 비밀 비노출 | `config/client.go`, `credentials.go`, canary/FIFO 테스트; 통합 TLS·비밀번호·잘못된 CA/auth 검사 |
| EP | 순차 전환, cluster/member 확인과 기존 cluster pin | `backup/source.go`, `publish.go`, `metadata.go`; headerless fixture, 신원 변경 단위 테스트; 실제 failover/mismatch 통합 |
| SNAP | Go Snapshot API, 공식 로컬 구조 검사 | `backup/source.go`, `inspect.go`; 실제 3.7.3 백업·로컬 verify·복원 읽기 |
| STORE | 0700/0600, 배타/공유 잠금, 공간 점검, 위험 경로 거부 | `storage/root_linux.go`; symlink/hardlink/FIFO/권한/잠금 및 외부 sentinel 테스트 |
| COMMIT | 내장·전체 SHA-256, strict metadata/manifest, sync, no-replace 확정 | `backup/publish.go`, `metadata.go`, `inspect.go`; write/sync/rename 충돌/최종 sync 오류 주입 |
| STATUS | DB 연결·전체 스냅샷 해싱 없는 상태 조회 | `backup/local.go`; 같은 크기 변조에서 status 성공/verify 실패 및 인증 자료 제거 후 오프라인 통합 |
| VERIFY | 전체/이름 지정 검사 및 보장 한계 | `backup/local.go`, `inspect.go`; snapshot/metadata/manifest 변조 및 helper timeout/출력 상한 테스트 |
| KEEP | 새 성공 후만 정리, 새 백업 고정, 안전한 삭제 대기 복구 | `backup/retention.go`; clock rollback, quarantine barrier, 단계별 실패/부분 삭제/충돌 테스트; 실제 keep=2 통합 |
| CANCEL | 실패·취소 시 기존 백업 보존 | 단위 오류 주입 및 helper kill/reap; 실제 잠금 획득 후 SIGTERM 취소와 이전 백업 바이트 보존 통합 |
| RESTORE | 별도 디렉터리·인스턴스·포트에서 복원 후 데이터 읽기 | `scripts/integration-test.py`의 공식 및 제품 snapshot restore 시나리오; [수동 훈련](restore-drill.md) |
| DEB | 바이너리·conffile·oneshot/timer·문서·라이선스 | `scripts/package-build.sh`, `packaging/systemd/*`, `packaging/tests/lifecycle.sh` |
| LIFE | 설치/업그레이드 자동 시작 금지, 설정/백업 보존 | 격리 Ubuntu의 실제 dpkg install→설정 수정→upgrade→remove→purge, static unit 검사 및 재현 빌드 |
| CI | 최소/최신 Go, 전체 통합·패키지 검증, 제한된 artifact | `.github/workflows/ci.yml`, `scripts/ci-bootstrap.sh`; action SHA 고정, actionlint 검사; hosted 결과는 Actions에서 확인 |
| DOC | 운영·복원·설치·한계 문서 | README, operations, restore-drill, packaging/integration/CI 안내; 문서 링크 및 패키지 문서 사본 검사 |

## 실행된 로컬 검사

- `go test -count=1 ./...`, `go test -race -count=1 ./...`, `go vet ./...`, 정적 Linux amd64 빌드 성공.
- 최종 제품 통합 **18개 시나리오 전부 PASS**, 소유한 컨테이너/fixture 정리 성공.
  TLS/mTLS·사용자 인증, all/named verify, 복원 읽기, failover/mismatch, keep=2,
  변조, 공간/연결/CA/auth 실패 보존, SIGTERM 취소, 인증 자료 없는 오프라인 명령을 포함.
- 해당 제품 바이너리 SHA-256:
  `435ff287b693e45f0cdeaba71aaeb57bceada153a27080a8e6ba322830a6b93b`.
- 최종 실제 바이너리 및 연결된 운영·복원·개발 문서를 포함한 격리 dpkg 수명주기 성공.
  설치→설정 수정→새 기본값과 충돌하는 upgrade→remove→purge에서 설정/백업 보존,
  비활성 unit, 정적 unit, 문서/라이선스 사본 및 동일 바이트 재현 빌드를 확인했다.
- `actionlint v1.7.12` 검사 성공. 공식 action tag와 full SHA 및 Node 24 요구 확인.
- `govulncheck v1.9.0`에서 호출 코드와 import package 취약점은 0건.
  전이 모듈 `golang.org/x/crypto`의 미사용 `openpgp` 경고 GO-2026-5932는 보고되지만
  해당 package를 import/호출하지 않는다. 이는 전체 보안 보증이 아니다.
- 독립 구현 리뷰와 한정된 보안 소스 리뷰에서 미해결 구체적 결함 없음.

## 명시적인 한계

- etcd 3.7.3의 실제 Snapshot 응답은 Header를 채우지 않는다. 선택 endpoint의
  전·후 Status 신원을 확인하고, 제공된 Header만 추가 검사한다. 스트림 자체의
  암호학적 출처 증명은 없으며 TLS와 endpoint 신뢰가 필요하다.
- 첫 실행에서 접근할 수 없는 후보의 신원은 알 수 없다.
- 악의적인 동일 UID/root, privileged mount 조작은 저장소 방어 범위 밖이다.
- 실제 스트림 중간 네트워크 단절의 결정적 통합 주입은 하지 않았다. 해당 실패·전환·
  기존 백업 보존은 엔진 단위 테스트가 별도로 검사한다.
- rootless Ubuntu에서 systemd 부팅과 timer disabled/inactive는 확인했지만, 실제
  service는 namespace 설정의 `CAP_SYS_ADMIN` 제한으로 217/USER 종료했다.
  패키지/static unit 성공을 서비스 sandbox·timer 실행 성공으로 해석하지 않는다.
  정상 대상 Ubuntu 노드에서 별도 검증해야 하며 이를 위해 보안 설정을 낮추지 않았다.
- 로컬 파일시스템 검사·mock 오류 주입은 운영 볼륨의 정전 내구성을 증명하지 않는다.
- 로컬 CI 문법·이미지 준비 검증은 hosted CI 성공을 대신하지 않는다.

## 공식 설계 근거

- [etcd 3.7.3 client Snapshot 구현](https://github.com/etcd-io/etcd/blob/v3.7.3/client/v3/maintenance.go)
- [etcd 3.7.3 서버 Snapshot 구현](https://github.com/etcd-io/etcd/blob/v3.7.3/server/etcdserver/api/v3rpc/maintenance.go)
- [공식 로컬 검사·복원 라이브러리](https://github.com/etcd-io/etcd/blob/v3.7.3/etcdutl/snapshot/v3_snapshot.go)
- [공식 복원 및 Kubernetes revision bump](https://etcd.io/docs/v3.7/op-guide/recovery/)
- [Go os.Root 제한](https://github.com/golang/go/blob/go1.26.0/src/os/root.go)
- [Linux open](https://man7.org/linux/man-pages/man2/open.2.html),
  [rename](https://man7.org/linux/man-pages/man2/rename.2.html),
  [fsync](https://man7.org/linux/man-pages/man2/fsync.2.html)
