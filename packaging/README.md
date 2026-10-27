# Ubuntu / Arch Linux 패키징

이 문서는 **패키지 개발자용 빌드·payload 계약·격리 시험** 안내다. 릴리스 패키지를
설치하는 운영자는 [빠른 시작](../docs/quickstart.md), 정기 운영·업그레이드·제거는
[운영 안내](../docs/operations.md#5-systemd-스케줄러와-패키지-수명주기)를 따른다.

대상은 **Ubuntu 24.04 amd64 (.deb)** 및 **Arch Linux x86_64 (.pkg.tar.zst)**다. 이 디렉터리는 Go 엔진과 독립적이다.
빌더는 명시적으로 전달받은 정적 ELF 실행 파일을 복사할 뿐 Go 빌드, 다운로드,
네트워크 접근, 호스트 패키지 설치, 서비스 실행을 하지 않는다. 패키지 검사는
실제 정적 Go 바이너리로 실행한다. 선택적인 C stub은 패키징 개발용 fixture이며
제품이나 실제 백업의 완료 증거가 아니다.

## 입력과 패키지 빌드

프로젝트 루트에서 다음처럼 실행한다. `--assets-dir`는 고정된 프로젝트 자료의
루트다. 해당 디렉터리에서 **README, quickstart·requirements·operations·troubleshooting·restore-drill 문서,
integration/CI 안내, config.example.toml, LICENSE, THIRD_PARTY_NOTICES만** 읽는다. unit과 이 문서는 빌더가 속한 checkout의
`packaging/`에서 읽는다. 변경 중인 checkout을 입력으로 사용하지 않는다.

```sh
scripts/package-build.sh \
  --binary /absolute/path/to/static-linux-amd64/etcd-backup-keeper \
  --version 20261027-1 \
  --output-dir "$PWD/packaging/artifacts/release" \
  --assets-dir /absolute/path/to/frozen-project-assets \
  --maintainer 'Actual package maintainer <actual-address@example.org>'
```

- 바이너리는 실행 가능한 일반 파일이어야 한다. ELF64/x86-64/little-endian,
  실행 파일 형식, interpreter 및 shared-library 의존성 부재를 `readelf`로
  검사한다. 빌드 단계에서는 실행하지 않는다. ELF 헤더만으로 Linux/Go 출처나
  CLI 호환성을 증명할 수 없으므로 실제 Go 바이너리의 출처·빌드 타깃은 별도로
  확인하고 아래 version smoke 및 실제 백업 검증을 수행해야 한다.
- 버전은 숫자로 시작하며 영숫자와 `. + ~`, 선택적인 `-revision`만 허용한다.
  Debian epoch 및 여러 `-`는 지원하지 않는다. 기존 결과 파일을 덮어쓰지 않는다.
  경로의 공백은 허용하지만 Podman mount 경로의 `:`/개행은 지원하지 않는다.
- `--maintainer`는 실제 패키지 관리자의 단일 `Name <email>` 필드다. 저작권자나
  라이선스의 선언이 아니다. 프로젝트는 BSD-3-Clause이며 저작권자는
  `LICENSE`의 Sangbum Kim이다. 해당 파일을 패키지 `copyright`로 포함하고,
  제품이 실제 import하는 Go 모듈과 Go 표준 라이브러리의 라이선스는
  `THIRD_PARTY_NOTICES`에 포함한다. 의존성 변경 시 `scripts/third-party-notices.sh`
  출력으로 이를 갱신한다. 테스트 maintainer는 예약된 `.invalid` 주소를 사용하며
  실제 배포 시 관리자의 주소를 명시해야 한다.
- 설정 입력은 자격 증명이 없는 example이어야 한다. 빌더가 비밀을 자동 탐지하거나
  제거해 주지는 않는다. 비밀번호·개인키·실제 호스트별 endpoint는 넣지 않는다.
- `SOURCE_DATE_EPOCH` 기본값은 `0`이다. 파일 timestamp, root 소유권, 압축 옵션을
  고정한다. 같은 입력/epoch/`dpkg-deb` 버전에서 동일 바이트를 목표로 한다.
  서로 다른 압축기/도구 버전 사이의 동일성은 보장하지 않는다.
- 호스트 `dpkg-deb`가 없으면 로컬 rootless Podman 이미지를 사용한다.
  기본값은 `localhost/etcd-backup-keeper-test-ubuntu:24.04`이며 `--image`로
  대체할 수 있다. Linux amd64 이미지가 미리 준비되어야 한다. `--pull=never`,
  `--network=none`, `dpkg-deb --root-owner-group`을 사용하며 root 호스트 설치는 없다.
  필요한 호스트 도구: Bash, GNU coreutils/findutils, `readelf`, 필요 시 Podman.

### 내용과 안전 경계

| 설치 경로 | 권한 / 의미 |
|---|---|
| `/usr/bin/etcd-backup-keeper` | root:root, 0755, 명시적으로 제공한 정적 바이너리 |
| `/etc/etcd-backup-keeper/` | root:root, 0700 |
| `/etc/etcd-backup-keeper/config.toml` | root:root, 0600, dpkg conffile |
| `/usr/lib/systemd/system/etcd-backup-keeper.{service,timer}` | root:root, 0644 |
| `/usr/share/doc/etcd-backup-keeper/` | README와 연결된 운영·복원·개발 안내, 설정 예시, LICENSE/copyright, THIRD_PARTY_NOTICES |

패키지에 백업 파일이나 `/var/lib` 디렉터리를 넣지 않는다. maintainer script,
`systemctl`, `deb-systemd-helper` 호출도 없다. 설치/업그레이드는 service/timer를
활성화·시작하지 않고 백업도 실행하지 않는다. 설정 변경의 유지/교체는 표준 dpkg
conffile 처리에 맡긴다. 변경된 기본 설정과 로컬 수정이 충돌하면 dpkg가 선택을
요청할 수 있다. 무인 업그레이드의 conffile 정책은 운영자가 명시해야 한다.
제거는 설정을 남기고 purge는 conffile을 제거하지만 **백업은 둘 다 보존**한다.
추가한 인증서·비밀번호 파일은 패키지 소유가 아니며 purge가 정리하지 않는다.

## Arch Linux 패키징

Deb과 **동일한 정적 Linux amd64 ELF**를 전달한다. 정적 CLI의 런타임에는 Go,
외부 `etcdctl`/`etcdutl`이 필요하지 않다. 패키지의 `depends=('systemd')`는 제공하는
service/timer용이며 etcd 서버를 설치하거나 시작하지 않는다. Arch 기본 도구 이미지가
미리 캐시되어 있어야 한다. 호스트 `makepkg`/`fakeroot`/`pacman`은 사용하지 않는다.

```sh
scripts/archpackage-build.sh \
  --binary /absolute/path/to/static-linux-amd64/etcd-backup-keeper \
  --version 20261027-1 \
  --assets-dir /absolute/path/to/frozen-project-assets \
  --output-dir "$PWD/packaging/artifacts/arch-release" \
  --packager 'Actual package maintainer <actual-address@example.org>'
```

- 버전은 유효한 날짜 `YYYYMMDD-PKGREL`만 받는다. `20261027-1`은
  `pkgver=20261027`, `pkgrel=1`, 아키텍처 `x86_64`로 매핑한다. pkgrel은 선행 0 없는
  1~999999다. `--packager`는 보수적인 영문 이름/이메일 단일 필드이며 필수다.
  문자열을 shell 코드로 평가하지 않는다. ELF·자산·epoch 검사는 Deb과 같은 경계다.
- 기본 이미지는 `docker.io/library/archlinux:base-devel`이며 `--image`로 캐시된
  Linux amd64 이미지를 지정할 수 있다. 필요한 도구는 makepkg, fakeroot, bsdtar,
  zstd, pacman, systemctl, systemd-analyze다. 이미지 준비는 별도 승인된 작업이다.
  빌드는 rootless Podman의 `--pull=never --network=none --userns=keep-id`, 비root
  사용자와 전용 writable HOME에서 수행한다. 호스트 도구 설치나 다운로드는 없다.
- 체크인된 `arch/PKGBUILD.in`은 소스 다운로드/컴파일 없이 명시적 payload만 복사한다.
  `strip`/`debug`를 비활성화해 제공된 바이너리 바이트를 유지한다. fakeroot가 archive를
  root:root로 기록하며 timestamp는 `SOURCE_DATE_EPOCH`(기본 0), zstd는 단일 thread다.
  같은 입력/epoch/**같은 도구 이미지**에서 두 빌드의 바이트 동일성을 시험한다.
  결과는 `.pkg.tar.zst`이며 기존 결과를 원자적으로 덮어쓰지 않고 거부한다.
- 설치 경로·권한은 위 표와 같다. 추가로 표준 Arch 라이선스 위치인
  `/usr/share/licenses/etcd-backup-keeper/LICENSE`에 LICENSE를 포함한다.
  `backup=('etc/etcd-backup-keeper/config.toml')`로 로컬 설정 변경을 보호한다.
  패키지 `.install` script, ALPM hook, 자동 enable/start, `/var` payload는 없다.
  시스템이 이미 제공하는 pacman hook은 동작할 수 있으나 이 패키지는 자동 활성화를
  요청하지 않는다. 실제 노드의 reload/stop/enable은 아래 운영 절차로 수행한다.
- 변경된 기본 템플릿과 운영자 수정이 충돌하면 기존 설정은 유지되고 새 기본값은
  `.pacnew`로 남는다. 수정 설정은 `pacman -R`에서 `.pacsave`로 보존된다.
  `--nosave`/`-Rn`은 이 보존 정책을 무시하므로 운영자가 별도로 판단해야 한다.
  백업 데이터나 별도 인증 자료는 패키지 소유가 아니며 제거하지 않는다.

### 격리된 Arch 수명주기 시험

```sh
scripts/archpackage-test.sh \
  --binary /absolute/path/to/static-linux-amd64/etcd-backup-keeper \
  --binary-kind real --assets-dir /absolute/path/to/frozen-project-assets \
  --version 20261027-1 --expected-version v20261027-1 \
  --evidence-dir "$PWD/packaging/test-results-arch-release"
```

`--expected-version`은 선택 사항이다(`dev` 또는 `vYYYYMMDD-PKGREL`). 릴리스 검증 시
명시하여 설치 바이너리의 실제 version 출력을 확인한다. 패키지 버전만으로 바이너리의
버전/출처를 보장하지 않는다. 시험 기본 패키지 버전은 `20261027-1`이고 upgrade는
자동으로 pkgrel+1이다. 예약된 테스트 packager를 사용하므로 산출물은 배포용이 아니다.

동일 패키지를 두 번 빌드해 `cmp`한 뒤 기본 템플릿만 바꾼 upgrade를 만든다.
폐기 가능한 rootless/network-off Arch 컨테이너에서 archive의 정확한 allowlist,
root 소유권, 버전/의존성/backup 메타데이터와 script/hook·`/var` 부재를 검사한 뒤
실제 `pacman -U` → 운영자 설정 수정 → upgrade → `pacman -R`를 수행한다.
이미지에 systemd 의존성이 있어 외부 저장소 접근은 필요 없다. 최소 이미지의 문서 제외는
이 패키지 doc subtree만 예외로 하여 설치 내용을 비교한다. `.pacnew`/`.pacsave`의
내용과 0600, 설정 디렉터리 0700, 설치 바이너리 hash, 문서/라이선스/unit 원본,
비활성 unit 상태, 패키지 비소유 백업 sentinel(0700/0600)을 전 단계에서 확인한다.
`systemd-analyze verify`와 `version`만 실행하며 backup/endpoint에는 접근하지 않는다.

빌드 staging과 컨테이너는 종료 시 정리한다. evidence에는 명시적 입력 자료 사본이
있으므로 비밀은 입력하지 않는다. CI는 허용된 log/manifest/pkg만 업로드한다.
**이 시험은 offline 패키지 검증이며 live systemd·실제 백업·복원을 보증하지 않는다.**

## 운영자가 명시적으로 활성화

패키지의 설치·업그레이드는 unit을 자동 활성화·시작하거나 백업을 실행하지 않는다.
수동 백업·검증·격리 복원과 실제 대상 호스트의 수용 확인 후 운영자가 활성화한다.
`Persistent=true`의 누락 실행 경고와 활성화 명령은 [빠른 시작](../docs/quickstart.md#5-복원-훈련수용-확인-후-timer-활성화),
stop·완료 대기·설정/백업 보존·hardening·timeout·쓰기 경로 변경은
[운영 수명주기 기준](../docs/operations.md#5-systemd-스케줄러와-패키지-수명주기)을 따른다.
아래 offline 시험은 그 운영 수용 검증을 대신하지 않는다.

## 격리된 수명주기 시험

실제 바이너리는 다음처럼 전달한다. 이미지에는 `dpkg`, `dpkg-deb`, Bash,
`systemctl`, `systemd-analyze`, `timeout`, tar 및 기본 GNU 도구가 필요하다.
이미지는 자동 pull/build/apt 설치하지 않는다. 결과 디렉터리는 새 경로여야 한다.

```sh
scripts/package-test.sh \
  --binary /absolute/path/to/static-linux-amd64/etcd-backup-keeper \
  --binary-kind real \
  --assets-dir /absolute/path/to/frozen-project-assets \
  --evidence-dir "$PWD/packaging/test-results-real"
```

이 시험은 패키징용 버전 `0.0.0~packaging1`/`0.0.0~packaging2`를 사용한다
(`--version`, `--upgrade-version`으로 변경 가능). 제품 버전과 같다는 주장은 아니다.
동일 입력을 두 번 빌드해 `cmp`하고, 컨테이너 내부에서 다음을 검사한다.

1. control에 maintainer script가 없고 conffile만 등록되었는지, archive root 소유권,
   `/var/lib` 미포함을 설치 전에 확인한다.
2. 실제 `dpkg --install` 후 파일/설정의 내용, private 권한, 문서, 바이너리 hash를
   확인한다. 최소 Ubuntu 이미지의 문서 제외 설정은 이 패키지의 문서 경로에만
   `--path-include`를 적용해 검사한다. `systemd-analyze verify`로 unit을 정적 검사한다.
3. `timeout 15 ... version`만 실행한다. **backup은 호출하지 않는다.**
4. 설정에 운영자 주석을 추가하고, 복사한 새 템플릿에 테스트 전용 주석을 추가해
   conffile 충돌을 만든다. `dpkg --force-confold --install`로 명시적으로 기존 설정을
   유지하고 `.dpkg-dist`의 새 기본값도 확인한다.
5. 실제 `dpkg --remove`에서 수정 설정을, `dpkg --purge`에서 설정 삭제를 확인한다.
   설치 전 만든 백업 sentinel의 내용/권한은 전 단계에서 보존되어야 한다.
6. 전 단계에서 enablement symlink 부재와 timer의 비활성 상태를 확인한다.
   stub 모드에서는 version 이외 호출이 남기는 marker도 없어야 한다.

컨테이너는 rootless, network off, systemd **offline**이다. 구조/수명주기 증거이지
실제 timer 실행, sandbox 작동, Go 엔진 동작, 스냅샷 성공/복원의 증거가 아니다.
전용 컨테이너는 정상/실패 종료 시 정리하고 로그 및 deb는 evidence 디렉터리에
남긴다. 빌드 staging 디렉터리도 정리한다. 증거에는 입력 파일 사본이 있으므로
example 외 민감한 파일을 입력하지 않는다.

### 제품이 없을 때만 쓰는 stub

정적 C 컴파일러가 있는 개발 환경에서 다음 fixture를 명시적으로 만든다.
빌더나 시험 스크립트는 stub을 자동 생성하거나 실제 바이너리로 가장하지 않는다.

```sh
mkdir -p packaging/artifacts
cc -static -O2 -o packaging/artifacts/packaging-stub packaging/tests/stub.c
scripts/package-test.sh \
  --binary "$PWD/packaging/artifacts/packaging-stub" --binary-kind stub \
  --assets-dir /absolute/path/to/frozen-project-assets \
  --evidence-dir "$PWD/packaging/test-results-stub"
```

stub은 `version`에 `PACKAGING TEST STUB`을 출력하며 나머지 명령을 거절한다.
이 결과로 제품을 승인하지 않는다. 통합 시 실제 정적 Go 바이너리와 갱신된 문서를
제공해 real 모드 빌드/수명주기 검증을 다시 수행하고, 별도 live systemd 환경에서
승인된 격리 etcd 대상의 service/timer/실제 백업·검증·복원까지 확인해야 한다.
