# Ubuntu 패키징

대상은 **Ubuntu 24.04 amd64**다. 이 디렉터리는 Go 엔진과 독립적이다.
빌더는 명시적으로 전달받은 정적 ELF 실행 파일을 복사할 뿐 Go 빌드, 다운로드,
네트워크 접근, 호스트 패키지 설치, 서비스 실행을 하지 않는다. 패키지 검사는
실제 정적 Go 바이너리로 실행한다. 선택적인 C stub은 패키징 개발용 fixture이며
제품이나 실제 백업의 완료 증거가 아니다.

## 입력과 패키지 빌드

프로젝트 루트에서 다음처럼 실행한다. `--assets-dir`는 고정된 프로젝트 자료의
루트다. 해당 디렉터리에서 **README, requirements·operations·restore-drill 문서,
integration/CI 안내, config.example.toml, LICENSE, THIRD_PARTY_NOTICES만** 읽는다. unit과 이 문서는 빌더가 속한 checkout의
`packaging/`에서 읽는다. 변경 중인 checkout을 입력으로 사용하지 않는다.

```sh
scripts/package-build.sh \
  --binary /absolute/path/to/static-linux-amd64/etcd-backup-keeper \
  --version 0.1.0 \
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

## 운영자가 명시적으로 활성화

아래는 offline 패키지 시험에서 실행하지 않는 **운영 절차**다. 실제 CLI 및
etcd 버전·TLS·권한·백업/복원 검증이 끝난 바이너리만 사용한다.

1. 패키지를 설치하고 private 설정을 편집한다. 기본 loopback endpoint도 의도한
   환경과 일치하는지 확인한다. 키·비밀번호는 패키지에 넣지 말고 별도로 배치한다.
2. root 실행은 kubeadm의 root 전용 인증서 접근을 위한 기본값이다. 인증서의
   소유권/권한과 private 백업 볼륨을 확인한다. 비밀을 CLI 인자로 전달하지 않는다.
3. live systemd 노드에서 운영자가 직접 수행한다:

   ```sh
   sudo systemctl daemon-reload
   # 승인된 환경에서 필요 시 수동 backup/verify 및 복원 훈련을 먼저 수행한다.
   sudo systemctl enable --now etcd-backup-keeper.timer
   systemctl list-timers etcd-backup-keeper.timer
   journalctl -u etcd-backup-keeper.service
   ```

`OnCalendar=daily`와 최대 15분의 무작위 지연을 사용한다. `Persistent=true`이므로
운영자가 timer를 시작하면 누락된 실행이 곧 수행될 수 있다. upgrade 뒤에도 운영자가
`daemon-reload`를 수행한다. 이미 실행 중인/활성화된 unit은 업그레이드가 중지하거나
재시작하지 않는다. remove/purge **전에** 운영자가 timer를 disable/stop하고 service의
완료를 기다리거나 명시적으로 stop해야 한다. 자동 중지를 위한 maintscript도 없다.

```sh
sudo systemctl disable --now etcd-backup-keeper.timer
sudo systemctl stop etcd-backup-keeper.service
# 이후 dpkg remove 또는 purge; 완료 후 운영자가 systemctl daemon-reload
```

service는 `UMask=0077`, `StateDirectoryMode=0700`, `ProtectSystem=strict`,
`ProtectHome=true`, 빈 capability set 등을 사용한다. systemd가 **실행 시** 기본
state 디렉터리를 만들며 패키지 설치가 생성하지 않는다. Go 네트워크 및 동일
실행 파일의 검사 helper를 막는 syscall/exec 제한은 넣지 않았다. 다만 실제
실행/sandbox 호환성은 live systemd 검증이 필요하다. `/home` 아래 자료는 숨겨진다.
다른 `backup_dir`을 쓰면 적절한 소유권/0700 권한으로 준비하고 unit drop-in의
`ReadWritePaths=`도 갱신한다. 기본 쓰기 허용 경로는 `/var/lib/etcd-backup-keeper`다.
`TimeoutStartSec=45min`이며 설정의 작업 timeout을 늘리면 unit도 함께 검토한다.

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
