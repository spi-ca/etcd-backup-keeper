# CI 검증 범위

[`workflows/ci.yml`](https://github.com/spi-ca/etcd-backup-keeper/blob/main/.github/workflows/ci.yml)은 `main` push, 일반 pull request, 수동 실행을
대상으로 한다. Ubuntu 24.04 amd64 GitHub-hosted VM에서 실행하며 배포·릴리스·
자동 커밋/푸시는 하지 않는다. 권한은 `contents: read`, checkout 자격 증명 저장은
비활성화한다. 별도 secret은 필요 없다. 같은 ref/PR의 이전 실행은 취소한다.

## 두 검증 lane

- **Go 1.26.x 최신 patch:** 최소 지원 minor line의 formatting, uncached test, vet.
  모듈의 `go 1.26.0` 계약은 유지하며 초기 unpatched toolchain으로 고정하지 않는다.
- **Go 1.27.x 최신 patch:** 일반 PR/main push/기본 수동 실행에서는 위 light 검사만 수행한다.
  **`release/`로 시작하는 head branch의 PR** 또는 수동 실행에서 boolean **`full=true`**를
  명시한 경우에만 race, `CGO_ENABLED=0` Linux amd64 빌드, 라이선스 고지 diff,
  전체 격리 etcd 3.7.3 통합, 재현 deb/Arch 패키지 빌드와 dpkg/pacman 수명주기를 추가한다.
  수동 실행의 `full` 기본값은 false다. 이미지/호스트 도구 준비도 같은 full 조건에서만
  실행한다. workflow run 이름과 각 lane 이름에 실제 full/light 범위를 표시한다.

`GOTOOLCHAIN=local`로 최소 버전 lane이 자동으로 새 toolchain을 사용하는 것을
막는다. module 파일은 readonly다. 제품 바이너리는 한 번 빌드하고 통합 및
deb/Arch 패키징 모두에 동일한 절대 경로로 전달한다. `SOURCE_DATE_EPOCH=0`을 사용한다.
릴리스 PR의 head ref는 `release/vYYYYMMDD-PKGREL`과 유효한 날짜/양의 pkgrel로 엄격히
검증한다(예: `release/v20261027-1`). 이 경우 `-X main.version=v20261027-1`을 주입하고
source SHA, binary/package version, 실제 version 출력과 바이너리 SHA256을 manifest에
기록한다. `release/` 접두사인데 형식이 틀리면 full build가 실패한다. 일반 수동 full은
`dev`일 수 있으며 릴리스 출처 검증을 대신하지 않는다. Arch lifecycle은 기대 바이너리
version도 확인한다. 정적 CLI에는 외부 etcd 도구나 Go 런타임이 필요하지 않으며 패키지의
systemd 의존성은 제공하는 service/timer를 위한 것이다.
고지가 오래되면 로컬에서 `scripts/third-party-notices.sh` 출력과 의존성 변경을
검토해 갱신한다. CI는 소스를 수정하거나 변경 사항을 커밋하지 않는다.

전체 통합 명령은 다음과 같다. evidence 디렉터리는 존재하지 않는 새 경로다.

```sh
python3 scripts/integration-test.py \
  --binary /absolute/path/to/static/etcd-backup-keeper \
  --evidence-dir /new/evidence/directory
```

기본 full 모드의 성공은 TLS/mTLS·인증·복원을 포함한 assertion의 skip 없는 성공을
요구한다. `--fixtures-only`는 별도의 선택적 오프라인 개발 모드이며 CI에서 사용하지
않는다. 패키지는 [`package-test.sh`](https://github.com/spi-ca/etcd-backup-keeper/blob/main/scripts/package-test.sh)의 `--binary-kind real`
모드를 사용한다. Arch도 [`archpackage-test.sh`](https://github.com/spi-ca/etcd-backup-keeper/blob/main/scripts/archpackage-test.sh)의 `--binary-kind real`로
동일 ELF를 사용해 재현성과 실제 offline pacman install/edit/upgrade/remove 및
`.pacnew`/`.pacsave` 설정 보존을 검사한다. 수명주기/재현성의 정확한 범위와 offline systemd의 한계는
[`packaging/README.md`](../packaging/README.md)가 기준이다. 실제 운영 systemd 실행이나
운영 etcd 접근을 증명하는 CI가 아니다.

## 일회성 VM 준비

[`scripts/ci-bootstrap.sh`](https://github.com/spi-ca/etcd-backup-keeper/blob/main/scripts/ci-bootstrap.sh)는 **GitHub-hosted Ubuntu 24.04
CI VM 전용**이다. 로컬 및 self-hosted 환경은 host 변경 전에 거부한다. 호스트에
Podman, uidmap, user-session 도구, Python 3, OpenSSL, binutils 및 dpkg 도구를 설치한다.
필요한 경우 기존 subordinate ID 범위를 건드리지 않고 새 범위를 할당한다.
rootless Podman의 cgroup v2 제한을 위해 사용자 systemd/DBus session을 준비한다.
namespace 제한으로 Podman preflight가 실패할 때만 `/usr/bin/podman`에 한정한
AppArmor userns profile을 로드한다. 전체 AppArmor 해제나 전역 sysctl 완화는 없다.
이 단계는 폐기 가능한 VM을 변경하므로 개발 호스트에서 환경 변수를 흉내 내 실행하지 않는다.

- 공식 etcd 이미지
  `gcr.io/etcd-development/etcd@sha256:b4a41c6864baf573f5213dce18b2a95e1002ec6c5a1a26ad900c2ea166ac6d0b`
  를 pull한 뒤 harness가 `--pull=never`로 사용하는 `:v3.7.3` 태그를 붙인다.
- [`ubuntu.Dockerfile`](https://github.com/spi-ca/etcd-backup-keeper/blob/main/packaging/tests/ubuntu.Dockerfile)은 digest 고정 공식 Ubuntu
  24.04에서 dpkg·Bash·CA·systemd 도구 이미지를 만든다. 빈 build context를 사용한다.
  로컬 태그는 `localhost/etcd-backup-keeper-test-ubuntu:24.04`다. apt 패키지 revision은
  Ubuntu security update를 따르므로 서로 다른 날짜의 이미지까지 byte 재현을 주장하지 않는다.
- 공식 Arch base-devel 이미지는
  `docker.io/library/archlinux@sha256:5b987b0196907ea97dd10e7b48e7d403c35c0389986da015aa86cf8d0b058084`로
  고정하여 `:base-devel`로 준비한다. makepkg/fakeroot/bsdtar/zstd/pacman/systemd 도구는
  컨테이너에만 있고 호스트 makepkg/fakeroot/pacman을 설치하거나 실행하지 않는다.
  빌드는 rootless keep-id의 비root 사용자/전용 writable HOME, lifecycle은 rootless
  컨테이너 내부 root에서 실행한다. 둘 다 `--pull=never --network=none`이다.
- 호스트 도구·이미지 준비에는 네트워크를 사용한다. 패키지 수명주기는 rootless/network-off이며
  etcd 통합은 harness가 생성한 격리 fixture만 사용한다.

## 결과와 action 갱신

14일 보관 artifact는 명시된 파일만 업로드한다: 도구 버전 log/빌드 manifest,
통합 최상위 **sanitized** `integration.log`/`summary.json`, 패키지 `lifecycle.log`/
`manifest.txt`, initial/upgrade `.deb` 및 Arch `lifecycle.log`/`manifest.txt`와
initial/upgrade `.pkg.tar.zst`. full 성공 시 같은 검증 바이너리도 별도 artifact에 보관하여
manifest hash와 비교할 수 있다. TLS 개인키, 비밀번호, 스냅샷, 원본 fixture와
패키지 asset 디렉터리, 전체 임시 트리는 업로드하지 않는다. 실패 시에도 존재하는
허용 파일은 업로드하지만 artifact 존재 자체가 검증 성공을 뜻하지 않는다.
`.deb`/`.pkg.tar.zst`는 예약된 테스트 maintainer/packager를 가진 **테스트 산출물**이며
배포용 maintainer/packager 또는 릴리스 게시가 아니다. deb lifecycle의 기본 패키징 버전은
`0.0.0~packaging1/2`, Arch는 release PR에서 검증한 버전/pkgrel+1(일반 수동 full은
`20261027-1/2`)이다. release PR 바이너리는 별도로 위의 실제 릴리스 버전을 갖는다.

공식 action은 full commit SHA로 고정한다. 현재 대응 tag는 checkout **v7.0.1**,
setup-go **v7.0.0**, upload-artifact **v7.0.2**다. 변경 시 각 공식 저장소의 tag→commit과
`action.yml`을 재확인한다. 모두 Node 24 action이며 runner v2.327.1 이상이 필요하다.
최초 hosted 실행에서 runner 버전, Go 배포 가용성, apt/registry 접근, rootless
namespace/cgroup 동작과 전체 통합·패키지 결과를 확인해야 한다. 문법 검사나 로컬
이미지 빌드만으로 GitHub CI의 성공을 대신하지 않는다.
