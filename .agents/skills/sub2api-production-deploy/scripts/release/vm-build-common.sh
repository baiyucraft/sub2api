#!/usr/bin/env bash
# Sourced only after the caller verifies this versioned asset's checksum.
# Neither metadata inspection nor a candidate build is retried here.

VM_BUILD_IMAGES=(
  docker.m.daocloud.io/library/node:24-alpine
  docker.m.daocloud.io/library/golang:1.27.2-alpine
  docker.m.daocloud.io/library/alpine:3.21
  docker.m.daocloud.io/library/postgres:18-alpine
)
VM_BUILD_IMAGE_ARGS=(
  --build-arg "NODE_IMAGE=${VM_BUILD_IMAGES[0]}"
  --build-arg "GOLANG_IMAGE=${VM_BUILD_IMAGES[1]}"
  --build-arg "ALPINE_IMAGE=${VM_BUILD_IMAGES[2]}"
  --build-arg "POSTGRES_IMAGE=${VM_BUILD_IMAGES[3]}"
)
VM_BUILD_FAILURE_CODE=none
VM_BUILD_DEPENDENCY_VERIFIED_COUNT=0

vm_build_docker() {
  # Keep both existing lists and all proxy variables; scope additions to Docker.
  local registry_no_proxy="${NO_PROXY:-}" registry_host
  if [[ -n ${no_proxy:-} ]]; then
    registry_no_proxy="${registry_no_proxy:+$registry_no_proxy,}${no_proxy}"
  fi
  for registry_host in docker.m.daocloud.io m.daocloud.io; do
    case ",$registry_no_proxy," in
      *,$registry_host,*) ;;
      *) registry_no_proxy="${registry_no_proxy:+$registry_no_proxy,}$registry_host" ;;
    esac
  done
  if [[ ${1:-} == buildx ]]; then
    DOCKER_BUILDKIT=1 NO_PROXY="$registry_no_proxy" no_proxy="$registry_no_proxy" timeout --kill-after=10s 90s docker "$@"
  else
    DOCKER_BUILDKIT=1 NO_PROXY="$registry_no_proxy" no_proxy="$registry_no_proxy" docker "$@"
  fi
}

vm_build_failure_code() {
  # Raw diagnostics stay in the caller's private files. Output is a fixed enum.
  local phase=$1 exit_code=$2
  shift 2
  if grep -Eiq 'no space left on device|disk quota exceeded|insufficient (disk|space)' "$@"; then
    printf '%s' build_space
  elif grep -Eiq 'authentication required|insufficient.scope|unauthorized:|requested access .*denied|status([[:space:]_-]+code)?[[:space:]:=]+(401|403)([^0-9]|$)|http(/[0-9.]+)?[[:space:]]+(401|403)([^0-9]|$)|401[[:space:]]+unauthorized|403[[:space:]]+forbidden' "$@"; then
    printf '%s' build_registry_auth
  elif [[ $exit_code == 124 || ( $phase == metadata && $exit_code == 137 ) ]] || grep -Eiq 'i/o timeout|context deadline exceeded|timed out|timeout awaiting|handshake timeout' "$@"; then
    printf '%s' build_timeout
  elif grep -Eiq 'x509:|tls:|tls handshake|certificate (verify|verification|has expired|signed by unknown)|ssl certificate' "$@"; then
    printf '%s' build_registry_tls
  elif grep -Eiq 'bad gateway|status([[:space:]_-]+code)?[[:space:]:=]+502([^0-9]|$)|http(/[0-9.]+)?[[:space:]]+502([^0-9]|$)' "$@"; then
    printf '%s' build_registry_502
  elif grep -Eiq 'unexpected eof|(^|[[:space:]:])eof([[:space:]]|$)' "$@"; then
    printf '%s' build_registry_eof
  elif [[ $phase == candidate ]] && grep -Eiq 'error TS[0-9]+:|syntax error|undefined:|cannot use .* as |compilation failed|build failed' "$@"; then
    printf '%s' build_compile
  else
    printf '%s' build_unknown
  fi
}

vm_build_private_directory() {
  local directory=$1 metadata current_uid
  [[ -d $directory && ! -L $directory ]] || return 1
  metadata=$(stat -c '%u:%a' "$directory") || return 1
  current_uid=$(id -u) || return 1
  [[ $metadata == "$current_uid:700" ]]
}

vm_build_new_log() {
  local path=$1
  [[ ! -e $path && ! -L $path ]] || return 1
  (umask 077; set -C; : >"$path") || return 1
}

vm_build_preflight() {
  local directory=$1 image index=0 metadata_log error_log exit_code
  VM_BUILD_FAILURE_CODE=build_dependency_contract
  VM_BUILD_DEPENDENCY_VERIFIED_COUNT=0
  vm_build_private_directory "$directory" || return 1
  for image in "${VM_BUILD_IMAGES[@]}"; do
    metadata_log="$directory/dependency-$index.json"
    error_log="$directory/dependency-$index.stderr"
    vm_build_new_log "$metadata_log" || return 1
    vm_build_new_log "$error_log" || return 1
    # Buildx uses Docker's configured registry/auth metadata resolver and no
    # image pull, application build, alternative registry, or fallback occurs.
    if vm_build_docker buildx imagetools inspect --raw "$image" >"$metadata_log" 2>"$error_log"; then
      :
    else
      exit_code=$?
      VM_BUILD_FAILURE_CODE=$(vm_build_failure_code metadata "$exit_code" "$error_log")
      return 1
    fi
    if ! jq -e '
      .schemaVersion == 2 and
      (.mediaType == "application/vnd.oci.image.index.v1+json" or .mediaType == "application/vnd.docker.distribution.manifest.list.v2+json") and
      (.manifests | type == "array") and
      ([.manifests[] | select(.platform.os == "linux" and .platform.architecture == "amd64")] |
       length == 1 and all(.[];
        (.mediaType == "application/vnd.oci.image.manifest.v1+json" or .mediaType == "application/vnd.docker.distribution.manifest.v2+json") and
        (.digest | type == "string" and test("^sha256:[0-9a-f]{64}$")) and
        (.size | type == "number" and . > 0 and floor == .)))
    ' "$metadata_log" >/dev/null 2>&1; then
      VM_BUILD_FAILURE_CODE=build_registry_metadata_invalid
      return 1
    fi
    VM_BUILD_DEPENDENCY_VERIFIED_COUNT=$((VM_BUILD_DEPENDENCY_VERIFIED_COUNT + 1))
    index=$((index + 1))
  done
  VM_BUILD_FAILURE_CODE=none
}

vm_build_candidate() {
  local log=$1 exit_code
  shift
  VM_BUILD_FAILURE_CODE=build_dependency_contract
  vm_build_private_directory "${log%/*}" || return 1
  vm_build_new_log "$log" || return 1
  if vm_build_docker build --network=host --progress=plain "${VM_BUILD_IMAGE_ARGS[@]}" "$@" >"$log" 2>&1; then
    VM_BUILD_FAILURE_CODE=none
  else
    exit_code=$?
    VM_BUILD_FAILURE_CODE=$(vm_build_failure_code candidate "$exit_code" "$log")
    return "$exit_code"
  fi
}
