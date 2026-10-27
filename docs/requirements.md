# 요구사항과 검증 매핑

현재 사용법은 [README](../README.md), 설정·실패 대응은 [운영 안내](operations.md)가
기준이다. 이 문서는 초기 범위가 구현과 검사에서 누락되지 않았는지 추적한다.

## 확정 범위

- Linux Go CLI, 최소 Go 1.26. 안정판 etcd 3.6.x/3.7.x 지원, 저장 형식은 3.6.0/3.7.0.
  서버/저장 버전은 독립적으로 허용하되 스트림/로컬 DB의 저장 형식은 일치해야 한다.
  다른 minor/major, prerelease, 알 수 없는 버전은 거부한다.
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
| CLI | backup/status/verify/check/version, 엄격 TOML, 사람용/JSON | `internal/cli/*`, `internal/config/*` 및 해당 테스트; MVP 통합 version/backup/verify/offline 시나리오와 후속 check/출력/도움말 대상 테스트 |
| AUTH | TLS/mTLS, private password_file, 비밀 비노출 | `config/client.go`, `credentials.go`, canary/FIFO 테스트; 통합 TLS·비밀번호·잘못된 CA/auth 검사 |
| EP | 순차 전환, cluster/member 확인과 기존 cluster pin | `backup/source.go`, `publish.go`, `metadata.go`; headerless fixture, 신원 변경 단위 테스트; 실제 failover/mismatch 통합 |
| SNAP | Go Snapshot API, 공식 로컬 구조 검사 | `etcdcompat/version.go`, `backup/source.go`, `inspect.go`; 양쪽 저장 형식의 합성 fixture 검사 및 실제 3.6.8/3.7.3 백업·로컬 verify·복원 읽기 |
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

### v20261027-2 호환성 변경

- 서버/저장 버전의 네 조합, 오프라인 verify, 미지원 버전 거부와 기존 백업 보존을
  단위·프로토콜 fixture 및 관련 Go 테스트/vet로 확인했다.
- 릴리즈 바이너리 SHA-256:
  `5cd040e1392a4ce78a819ec091dc1a6a1a849bad15b895034d6aa3e6287815f1`.
- 동일 바이너리로 공식 **3.6.8 / 3.7.3** 각각의 전체 **18개 통합 시나리오 PASS**,
  컨테이너/fixture 정리 성공. TLS/mTLS·인증, 백업·verify, 별도 인스턴스 복원 읽기,
  failover·신원 불일치·실패 시 보존·보관 개수·취소·오프라인 명령을 포함한다.
- 버전 20261027-2 패키지의 Debian·Arch 재현 빌드와 격리 설치·설정 수정·업그레이드·
  제거를 확인했다. Debian purge, Arch `.pacnew`/`.pacsave`, private 설정·백업 보존,
  비활성 unit과 정적 unit 검사를 포함한다. 실제 서비스 실행 성공을 뜻하지 않는다.
- `actionlint v1.7.12`, Bash/Python 문법 검사와 독립 읽기 전용 구현 리뷰를 통과했다.
- 공식 이미지 digest와 격리 `etcd --version` 출처는
  [CI 준비 기록](../.github/README.md#일회성-vm-준비)에 있다.

### 초기 MVP 검증 기록 (3.7.3)

아래 기록은 당시 바이너리의 결과이며, 변경된 바이너리의 검증을 대신하지 않는다.

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

## 후속 사용성 개선 검증

- 진행 로그, 정적 오류별 복구 안내, 사람용 상태 요약, 명령별 도움말과 README의 첫 실행 흐름을 개선했다.
- 새 `check`는 기본 오프라인이며 명시된 인증 자료를 읽고 저장 경로를 읽기 전용으로 점검한다.
  `--connect`만 후보별 Status 접속을 허용한다. 기존 `status`/`verify`는 인증 자료도 읽지 않는다.
- 관련 단위 테스트·fake clock 기반 시간 검사·대상 vet 및 실행 파일 빌드로 확인했다.
  이전 MVP 전체 통합·패키지 성공을 이 변경의 전체 회귀 검증으로 간주하지 않는다.
- `check`의 확인됨·미확인·실패 의미와 보장하지 않는 조건은 [운영 안내](operations.md)를 따른다.

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
