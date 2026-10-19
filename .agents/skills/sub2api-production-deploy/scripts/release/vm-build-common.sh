#!/usr/bin/env bash
# Sourced only after the caller verifies this versioned asset's checksum.
# Metadata resolution, dependency materialization and candidate build are never retried here.

VM_BUILD_IMAGES=(
  docker.io/library/node:24-alpine
  docker.io/library/golang:1.27.2-alpine
  docker.io/library/alpine:3.21
  docker.io/library/postgres:18-alpine
)
VM_BUILD_FRONTEND=docker.io/docker/dockerfile:1.7
VM_BUILD_PLATFORM=linux/amd64
VM_BUILD_BUILDER=default
VM_BUILD_HELPER_PATH=${BASH_SOURCE[0]}
VM_BUILD_ROLES=(node golang alpine postgres frontend)
VM_BUILD_FAILURE_CODE=none
VM_BUILD_FAILURE_PHASE=none
VM_BUILD_FAILURE_ROLE=none
VM_BUILD_FAILURE_METHOD=none
VM_BUILD_DEPENDENCY_VERIFIED_COUNT=0
VM_BUILD_PROOF_PATH=
VM_BUILD_PROOF_SHA256=
VM_BUILD_RELEASE_ID=
VM_BUILD_COMMIT=
VM_BUILD_DOCKERFILE=
VM_BUILD_IMAGE_ARGS=()

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
  if [[ ${1:-} == buildx && ${2:-} == build ]]; then
    if [[ ${VM_BUILD_OPERATION:-candidate} == dependency ]]; then
      DOCKER_BUILDKIT=1 NO_PROXY="$registry_no_proxy" no_proxy="$registry_no_proxy" timeout --kill-after=30s 1200s docker "$@"
    else
      DOCKER_BUILDKIT=1 NO_PROXY="$registry_no_proxy" no_proxy="$registry_no_proxy" docker "$@"
    fi
  else
    DOCKER_BUILDKIT=1 NO_PROXY="$registry_no_proxy" no_proxy="$registry_no_proxy" timeout --kill-after=10s 90s docker "$@"
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

# Imagetools may append a single LF to raw content. Only the descriptor's exact
# byte length and digest can establish which bytes are registry content.
vm_build_verify_content() {
  local path=$1 digest=$2 size=$3 actual_size actual_digest
  [[ $digest =~ ^sha256:[0-9a-f]{64}$ && $size =~ ^[1-9][0-9]*$ && $size -le 4194304 ]] || return 1
  actual_size=$(stat -c '%s' "$path") || return 1
  if [[ $actual_size == "$((size + 1))" ]]; then
    [[ $(tail -c 1 "$path" | od -An -tu1 | tr -d ' \n') == 10 ]] || return 1
    truncate -s "$size" "$path" || return 1
  elif [[ $actual_size != "$size" ]]; then
    return 1
  fi
  actual_digest=$(sha256sum "$path" | awk '{print $1}') || return 1
  [[ sha256:$actual_digest == "$digest" ]]
}

vm_build_inspect() {
  local log=$1 error_log=$2 exit_code
  shift 2
  vm_build_new_log "$log" && vm_build_new_log "$error_log" || return 1
  if vm_build_docker buildx imagetools inspect "$@" >"$log" 2>"$error_log"; then
    return 0
  else
    exit_code=$?
    VM_BUILD_FAILURE_CODE=$(vm_build_failure_code metadata "$exit_code" "$error_log")
    vm_build_transport_method "$error_log"
    return 1
  fi
}

vm_build_transport_method() {
  VM_BUILD_FAILURE_METHOD=unknown
  if grep -Eq '(^|[[:space:]])(HEAD|Head)[[:space:]"]+https?://' "$1"; then
    VM_BUILD_FAILURE_METHOD=HEAD
  elif grep -Eq '(^|[[:space:]])(GET|Get)[[:space:]"]+https?://' "$1"; then
    VM_BUILD_FAILURE_METHOD=GET
  fi
}

vm_build_capture_identity() {
  local directory=$1 context identity_sha
  vm_build_private_directory "$directory" || return 1
  # These private files may contain endpoint names, so they never cross SSH.
  vm_build_new_log "$directory/builder.raw" && vm_build_new_log "$directory/daemon.raw" &&
    vm_build_new_log "$directory/context.raw" && vm_build_new_log "$directory/identity.stderr" || return 1
  [[ $VM_BUILD_BUILDER == default && $VM_BUILD_PLATFORM == linux/amd64 ]] || return 1
  # Buildx inspect has no --format option on the supported VM version. Parse
  # only these anchored, unique fields; never hash volatile Last Activity/GC.
  vm_build_docker buildx inspect "$VM_BUILD_BUILDER" >"$directory/builder.raw" 2>"$directory/identity.stderr" || return 1
  jq -Rsc '
    split("\n") | map(select(test("^(Name|Driver|Endpoint|Status|BuildKit version|Platforms):"))) |
    map(capture("^(?<key>[^:]+):[ \\t]*(?<value>.*)$")) | . as $fields |
    def values($k): [$fields[] | select(.key==$k) | .value];
    if (values("Name")|length)==2 and (values("Driver")|length)==1 and
       (values("Endpoint")|length)==1 and (values("Status")|length)==1 and
       (values("BuildKit version")|length)==1 and (values("Platforms")|length)==1 then
      {name:values("Name")[0],driver:values("Driver")[0],node:{name:values("Name")[1],
       endpoint:values("Endpoint")[0],status:values("Status")[0],version:values("BuildKit version")[0],platforms:values("Platforms")[0]}}
    else error("builder contract") end
  ' "$directory/builder.raw" >"$directory/builder.parsed.json" 2>>"$directory/identity.stderr" || return 1
  vm_build_docker info --format '{{json .ID}} {{json .OSType}} {{json .Architecture}} {{json .ServerVersion}}' >"$directory/daemon.raw" 2>>"$directory/identity.stderr" || return 1
  context=$(vm_build_docker context show 2>>"$directory/identity.stderr") || return 1
  vm_build_docker context inspect "$context" --format '{{json .Endpoints.docker}}' >"$directory/context.raw" 2>>"$directory/identity.stderr" || return 1
  jq -e --arg name "$VM_BUILD_BUILDER" '
    .name == $name and .driver == "docker" and .node.status == "running" and
    (.node.platforms | test("(^|[ ,])linux/amd64([*, /]|$)"))
  ' "$directory/builder.parsed.json" >/dev/null 2>&1 || return 1
  jq -se 'length == 4 and (.[0] | type == "string" and length > 0) and .[1] == "linux" and (.[2] == "x86_64" or .[2] == "amd64")' "$directory/daemon.raw" >/dev/null 2>&1 || return 1
  jq -cnS --slurpfile b "$directory/builder.parsed.json" --slurpfile d "$directory/daemon.raw" --slurpfile c "$directory/context.raw" \
    --arg context "$context" --arg host "${DOCKER_HOST:-}" --arg dc "${DOCKER_CONTEXT:-}" \
    --arg tls "${DOCKER_TLS_VERIFY:-}" --arg cert "${DOCKER_CERT_PATH:-}" --arg config "${DOCKER_CONFIG:-}" '
    {builder:$b[0],
     daemon:$d,context:$context,endpoint:$c[0],selection:{host:$host,context:$dc,tls:$tls,cert:$cert,config:$config}}
  ' >"$directory/identity.private.json" 2>>"$directory/identity.stderr" || return 1
  chmod 600 "$directory/identity.private.json" || return 1
  identity_sha=$(sha256sum "$directory/identity.private.json" | awk '{print $1}') || return 1
  jq -cnS --arg name "$VM_BUILD_BUILDER" --arg hash "$identity_sha" '{name:$name,driver:"docker",identity_sha256:$hash}' >"$directory/builder.json" || return 1
  chmod 600 "$directory/builder.json"
}

vm_build_lock_role() {
  local directory=$1 role=$2 image=$3 repository root_digest root_size root_type digest size child_type manifest
  repository=${image%:*}
  VM_BUILD_FAILURE_CODE=build_registry_metadata_invalid
  VM_BUILD_FAILURE_PHASE=metadata
  VM_BUILD_FAILURE_ROLE=$role
  VM_BUILD_FAILURE_METHOD=unknown
  vm_build_inspect "$directory/$role-descriptor.json" "$directory/$role-descriptor.stderr" --format '{{json .Manifest}}' "$image" || return 1
  jq -se 'length==1' "$directory/$role-descriptor.json" >/dev/null 2>&1 || return 1
  jq -e '
    (.digest | type == "string" and test("^sha256:[0-9a-f]{64}$")) and
    (.size | type == "number" and . > 0 and . <= 4194304 and floor == .) and
    (.mediaType == "application/vnd.oci.image.index.v1+json" or .mediaType == "application/vnd.docker.distribution.manifest.list.v2+json")
  ' "$directory/$role-descriptor.json" >/dev/null 2>&1 || return 1
  root_digest=$(jq -r .digest "$directory/$role-descriptor.json") || return 1
  root_size=$(jq -r .size "$directory/$role-descriptor.json") || return 1
  root_type=$(jq -r .mediaType "$directory/$role-descriptor.json") || return 1
  vm_build_inspect "$directory/$role-index.json" "$directory/$role-index.stderr" --raw "$repository@$root_digest" || return 1
  vm_build_verify_content "$directory/$role-index.json" "$root_digest" "$root_size" || return 1
  jq -se 'length==1' "$directory/$role-index.json" >/dev/null 2>&1 || return 1
  jq -e --arg media "$root_type" '
    .schemaVersion == 2 and .mediaType == $media and (.manifests | type == "array") and
    ([.manifests[] | select(.platform.os == "linux" and .platform.architecture == "amd64")] |
      length == 1 and all(.[];
        (.mediaType == "application/vnd.oci.image.manifest.v1+json" or .mediaType == "application/vnd.docker.distribution.manifest.v2+json") and
        (.digest | type == "string" and test("^sha256:[0-9a-f]{64}$")) and
        (.size | type == "number" and . > 0 and . <= 4194304 and floor == .)))
  ' "$directory/$role-index.json" >/dev/null 2>&1 || return 1
  digest=$(jq -r '.manifests[] | select(.platform.os == "linux" and .platform.architecture == "amd64") | .digest' "$directory/$role-index.json") || return 1
  size=$(jq -r '.manifests[] | select(.platform.os == "linux" and .platform.architecture == "amd64") | .size' "$directory/$role-index.json") || return 1
  child_type=$(jq -r '.manifests[] | select(.platform.os == "linux" and .platform.architecture == "amd64") | .mediaType' "$directory/$role-index.json") || return 1
  manifest="$directory/$role-manifest.json"
  VM_BUILD_FAILURE_PHASE=manifest
  vm_build_inspect "$manifest" "$directory/$role-manifest.stderr" --raw "$repository@$digest" || return 1
  vm_build_verify_content "$manifest" "$digest" "$size" || return 1
  jq -se 'length==1' "$manifest" >/dev/null 2>&1 || return 1
  jq -e --arg media "$child_type" '
    def descriptor: (.digest | type == "string" and test("^sha256:[0-9a-f]{64}$")) and
      (.size | type == "number" and . > 0 and . <= 10737418240 and floor == .);
    .schemaVersion == 2 and .mediaType == $media and
    (.mediaType == "application/vnd.oci.image.manifest.v1+json" or .mediaType == "application/vnd.docker.distribution.manifest.v2+json") and
    (.config | descriptor and (.mediaType == "application/vnd.oci.image.config.v1+json" or .mediaType == "application/vnd.docker.container.image.v1+json")) and
    (.layers | type == "array" and length > 0 and length <= 128 and all(.[]; descriptor and
      (.mediaType == "application/vnd.oci.image.layer.v1.tar+gzip" or .mediaType == "application/vnd.oci.image.layer.v1.tar" or
       .mediaType == "application/vnd.oci.image.layer.v1.tar+zstd" or .mediaType == "application/vnd.docker.image.rootfs.diff.tar.gzip")))
  ' "$manifest" >/dev/null 2>&1 || return 1
  jq -cS --arg ref "$image" --arg root "$root_digest" --arg digest "$digest" --arg pinned "$repository@$digest" '
    {declared_ref:$ref,index_digest:$root,manifest_digest:$digest,pinned_ref:$pinned,
     config:(.config|{digest,size,media_type:.mediaType}),layers:[.layers[]|{digest,size,media_type:.mediaType}]}
  ' "$manifest" >"$directory/$role-lock.json" || return 1
  chmod 600 "$directory/$role-lock.json"
}

vm_build_proof_shape() {
  local proof=$1
  jq -se 'length==1' "$proof" >/dev/null 2>&1 || return 1
  jq -e --arg node "${VM_BUILD_IMAGES[0]}" --arg golang "${VM_BUILD_IMAGES[1]}" \
    --arg alpine "${VM_BUILD_IMAGES[2]}" --arg postgres "${VM_BUILD_IMAGES[3]}" --arg frontend "$VM_BUILD_FRONTEND" '
    def sha: type == "string" and test("^[0-9a-f]{64}$");
    def digest: type == "string" and test("^sha256:[0-9a-f]{64}$");
    def blob: keys == ["digest","media_type","size"] and (.digest|digest) and
      (.media_type|type=="string") and (.size|type=="number" and .>0 and .<=10737418240 and floor==.);
    keys == ["builder","commit_sha","dependency_solve_count","dockerfile_sha256","helper_sha256","materialized_sha256","platform","release_id","roles","schema"] and
    .schema == 1 and .dependency_solve_count == 1 and .platform == "linux/amd64" and
    (.release_id|type=="string" and test("^[0-9]+-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}$")) and
    (.commit_sha|type=="string" and test("^[0-9a-f]{40}$")) and (.dockerfile_sha256|sha) and (.helper_sha256|sha) and
    (.builder|keys == ["driver","identity_sha256","name"] and .driver=="docker" and .name=="default" and (.identity_sha256|sha)) and
    (.materialized_sha256|keys == ["alpine","golang","node","postgres"] and all(.[];sha)) and
    (.roles|keys == ["alpine","frontend","golang","node","postgres"]) and
    (.roles as $roles | {node:$node,golang:$golang,alpine:$alpine,postgres:$postgres,frontend:$frontend} | to_entries | all(.[];
      . as $expected | $roles[$expected.key] |
      keys == ["config","declared_ref","index_digest","layers","manifest_digest","pinned_ref"] and
      .declared_ref == $expected.value and (.index_digest|digest) and (.manifest_digest|digest) and
      .pinned_ref == ((.declared_ref|sub(":[^/:]+$";"")) + "@" + .manifest_digest) and
      (.config|blob and (.media_type == "application/vnd.oci.image.config.v1+json" or .media_type == "application/vnd.docker.container.image.v1+json")) and
      (.layers|type=="array" and length>0 and length<=128 and all(.[];blob and
        (.media_type == "application/vnd.oci.image.layer.v1.tar+gzip" or .media_type == "application/vnd.oci.image.layer.v1.tar" or
         .media_type == "application/vnd.oci.image.layer.v1.tar+zstd" or .media_type == "application/vnd.docker.image.rootfs.diff.tar.gzip")))))
  ' "$proof" >/dev/null 2>&1
}

vm_build_preflight() {
  local directory=$1 release_id=$2 commit=$3 dockerfile=$4 role index=0 exit_code hash file marker materialized='{}'
  local VM_BUILD_OPERATION=dependency
  VM_BUILD_FAILURE_CODE=build_dependency_contract
  VM_BUILD_FAILURE_PHASE=contract
  VM_BUILD_FAILURE_ROLE=none
  VM_BUILD_FAILURE_METHOD=unknown
  VM_BUILD_DEPENDENCY_VERIFIED_COUNT=0
  VM_BUILD_PROOF_PATH=
  [[ $VM_BUILD_PLATFORM == linux/amd64 && $VM_BUILD_BUILDER == default ]] || return 1
  [[ $release_id =~ ^[0-9]+-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}$ && $commit =~ ^[0-9a-f]{40}$ && $release_id == *-"${commit:0:12}"-* ]] || return 1
  vm_build_private_directory "$directory" || return 1
  [[ -f $dockerfile && ! -L $dockerfile && -f $VM_BUILD_HELPER_PATH && ! -L $VM_BUILD_HELPER_PATH ]] || return 1
  [[ $(head -n 1 "$dockerfile") == '# syntax=docker/dockerfile:1.7' ]] || return 1
  install -d -m 700 "$directory/identity-preflight" || return 1
  VM_BUILD_FAILURE_PHASE=identity
  vm_build_capture_identity "$directory/identity-preflight" || return 1
  for role in "${VM_BUILD_ROLES[@]}"; do
    if [[ $role == frontend ]]; then file=$VM_BUILD_FRONTEND; else file=${VM_BUILD_IMAGES[$index]}; fi
    vm_build_lock_role "$directory" "$role" "$file" || return 1
    VM_BUILD_DEPENDENCY_VERIFIED_COUNT=$((VM_BUILD_DEPENDENCY_VERIFIED_COUNT + 1))
    index=$((index + 1))
  done
  # Each source is reachable from the exported stage, so BuildKit cannot prune
  # the source or satisfy this validation with a manifest-only operation.
  vm_build_new_log "$directory/Dependency.Dockerfile" && vm_build_new_log "$directory/solve.log" || return 1
  {
    printf '# syntax=docker/dockerfile:1.7\n'
    for role in node golang alpine postgres; do
      printf 'FROM %s AS %s\n' "$(jq -r .pinned_ref "$directory/$role-lock.json")" "$role"
    done
    printf 'FROM scratch\n'
    for role in node golang alpine postgres; do printf 'COPY --from=%s /etc/os-release /%s\n' "$role" "$role"; done
  } >"$directory/Dependency.Dockerfile" || return 1
  VM_BUILD_FAILURE_PHASE=dependency
  VM_BUILD_FAILURE_ROLE=none
  VM_BUILD_FAILURE_METHOD=unknown
  if vm_build_docker buildx build --builder "$VM_BUILD_BUILDER" --platform "$VM_BUILD_PLATFORM" --progress=plain \
    --build-arg "BUILDKIT_SYNTAX=$(jq -r .pinned_ref "$directory/frontend-lock.json")" \
    --output "type=local,dest=$directory/materialized" -f "$directory/Dependency.Dockerfile" "$directory" >"$directory/solve.log" 2>&1; then
    :
  else
    exit_code=$?
    VM_BUILD_FAILURE_CODE=$(vm_build_failure_code dependency "$exit_code" "$directory/solve.log")
    vm_build_transport_method "$directory/solve.log"
    return 1
  fi
  VM_BUILD_FAILURE_CODE=build_preflight_unproven
  [[ -d $directory/materialized && ! -L $directory/materialized && $(find "$directory/materialized" -mindepth 1 -maxdepth 1 | wc -l) == 4 ]] || return 1
  for role in node golang alpine postgres; do
    marker="$directory/materialized/$role"
    [[ -f $marker && ! -L $marker && $(stat -c '%s' "$marker") -gt 0 && $(stat -c '%s' "$marker") -le 65536 ]] || return 1
    hash=$(sha256sum "$marker" | awk '{print $1}') || return 1
    materialized=$(jq -cn --argjson old "$materialized" --arg role "$role" --arg hash "$hash" '$old+{($role):$hash}') || return 1
  done
  vm_build_new_log "$directory/proof.json" || return 1
  jq -cnS --arg release "$release_id" --arg commit "$commit" \
    --arg dockerfile "$(sha256sum "$dockerfile" | awk '{print $1}')" --arg helper "$(sha256sum "$VM_BUILD_HELPER_PATH" | awk '{print $1}')" \
    --slurpfile builder "$directory/identity-preflight/builder.json" --argjson materialized "$materialized" \
    --slurpfile node "$directory/node-lock.json" --slurpfile golang "$directory/golang-lock.json" \
    --slurpfile alpine "$directory/alpine-lock.json" --slurpfile postgres "$directory/postgres-lock.json" --slurpfile frontend "$directory/frontend-lock.json" '
    {schema:1,release_id:$release,commit_sha:$commit,platform:"linux/amd64",dockerfile_sha256:$dockerfile,helper_sha256:$helper,
     builder:$builder[0],roles:{node:$node[0],golang:$golang[0],alpine:$alpine[0],postgres:$postgres[0],frontend:$frontend[0]},
     materialized_sha256:$materialized,dependency_solve_count:1}
  ' >"$directory/proof.json" || return 1
  chmod 400 "$directory/proof.json" || return 1
  vm_build_proof_shape "$directory/proof.json" || return 1
  VM_BUILD_FAILURE_CODE=none
  VM_BUILD_FAILURE_PHASE=none
  VM_BUILD_FAILURE_ROLE=none
  VM_BUILD_FAILURE_METHOD=none
}

vm_build_use_proof() {
  local proof=$1 expected_sha=$2 release_id=$3 commit=$4 dockerfile=$5 identity_dir actual_sha
  VM_BUILD_FAILURE_CODE=build_dependency_contract
  VM_BUILD_FAILURE_PHASE=contract
  VM_BUILD_FAILURE_ROLE=none
  VM_BUILD_FAILURE_METHOD=unknown
  [[ $VM_BUILD_PLATFORM == linux/amd64 && $VM_BUILD_BUILDER == default ]] || return 1
  [[ $expected_sha =~ ^[0-9a-f]{64}$ && -f $proof && ! -L $proof && -f $dockerfile && ! -L $dockerfile ]] || return 1
  vm_build_private_directory "${proof%/*}" || return 1
  [[ $(stat -c '%u:%a:%h' "$proof") == "$(id -u):400:1" ]] || return 1
  actual_sha=$(sha256sum "$proof" | awk '{print $1}') || return 1
  [[ $actual_sha == "$expected_sha" ]] || return 1
  vm_build_proof_shape "$proof" || return 1
  jq -e --arg release "$release_id" --arg commit "$commit" \
    --arg dockerfile "$(sha256sum "$dockerfile" | awk '{print $1}')" --arg helper "$(sha256sum "$VM_BUILD_HELPER_PATH" | awk '{print $1}')" '
    .release_id==$release and .commit_sha==$commit and .dockerfile_sha256==$dockerfile and .helper_sha256==$helper
  ' "$proof" >/dev/null 2>&1 || return 1
  identity_dir=$(mktemp -d "${proof%/*}/identity-check.XXXXXXXX") || return 1
  VM_BUILD_FAILURE_PHASE=identity
  vm_build_capture_identity "$identity_dir" || return 1
  jq -e --slurpfile current "$identity_dir/builder.json" '.builder==$current[0]' "$proof" >/dev/null 2>&1 || return 1
  VM_BUILD_IMAGE_ARGS=(
    --build-arg "NODE_IMAGE=$(jq -r .roles.node.pinned_ref "$proof")"
    --build-arg "GOLANG_IMAGE=$(jq -r .roles.golang.pinned_ref "$proof")"
    --build-arg "ALPINE_IMAGE=$(jq -r .roles.alpine.pinned_ref "$proof")"
    --build-arg "POSTGRES_IMAGE=$(jq -r .roles.postgres.pinned_ref "$proof")"
    --build-arg "BUILDKIT_SYNTAX=$(jq -r .roles.frontend.pinned_ref "$proof")"
  )
  VM_BUILD_PROOF_PATH=$proof
  VM_BUILD_PROOF_SHA256=$expected_sha
  VM_BUILD_RELEASE_ID=$release_id
  VM_BUILD_COMMIT=$commit
  VM_BUILD_DOCKERFILE=$dockerfile
  VM_BUILD_FAILURE_CODE=none
  VM_BUILD_FAILURE_PHASE=none
  VM_BUILD_FAILURE_ROLE=none
  VM_BUILD_FAILURE_METHOD=none
}

vm_build_candidate() {
  local log=$1 exit_code argument commit_count=0 version_count=0 date_count=0 tag_count=0 context_count=0
  shift
  local -a candidate_args=("$@")
  VM_BUILD_FAILURE_CODE=build_dependency_contract
  VM_BUILD_FAILURE_PHASE=contract
  VM_BUILD_FAILURE_ROLE=none
  VM_BUILD_FAILURE_METHOD=unknown
  vm_build_private_directory "${log%/*}" || return 1
  vm_build_new_log "$log" || return 1
  [[ -n $VM_BUILD_PROOF_PATH ]] || return 1
  vm_build_use_proof "$VM_BUILD_PROOF_PATH" "$VM_BUILD_PROOF_SHA256" "$VM_BUILD_RELEASE_ID" "$VM_BUILD_COMMIT" "$VM_BUILD_DOCKERFILE" || return 1
  VM_BUILD_FAILURE_CODE=build_dependency_contract
  VM_BUILD_FAILURE_PHASE=contract
  VM_BUILD_FAILURE_METHOD=unknown
  [[ $(readlink -f "$VM_BUILD_DOCKERFILE") == "$(readlink -f ./Dockerfile)" ]] || return 1
  # Callers may supply identity/tag only; later flags must not override locked
  # FROM/frontend references, builder, platform, Dockerfile or exporter.
  while (( $# )); do
    argument=$1
    shift
    case $argument in
      --build-arg)
        (( $# )) || return 1
        case $1 in
          COMMIT=*) [[ $1 == "COMMIT=$VM_BUILD_COMMIT" ]] || return 1; commit_count=$((commit_count+1));;
          VERSION=?*) version_count=$((version_count+1));;
          DATE=?*) date_count=$((date_count+1));;
          *) return 1;;
        esac
        shift;;
      -t) (( $# )) && [[ -n $1 ]] || return 1; tag_count=$((tag_count+1)); shift;;
      .) (( $# == 0 )) || return 1; context_count=$((context_count+1));;
      *) return 1;;
    esac
  done
  [[ $commit_count == 1 && $version_count == 1 && $date_count == 1 && $tag_count == 1 && $context_count == 1 ]] || return 1
  VM_BUILD_FAILURE_PHASE=candidate
  if vm_build_docker buildx build --builder "$VM_BUILD_BUILDER" --platform "$VM_BUILD_PLATFORM" --load \
    --network=host --progress=plain "${VM_BUILD_IMAGE_ARGS[@]}" "${candidate_args[@]}" >"$log" 2>&1; then
    VM_BUILD_FAILURE_CODE=none
    VM_BUILD_FAILURE_PHASE=none
    VM_BUILD_FAILURE_ROLE=none
    VM_BUILD_FAILURE_METHOD=none
  else
    exit_code=$?
    VM_BUILD_FAILURE_CODE=$(vm_build_failure_code candidate "$exit_code" "$log")
    vm_build_transport_method "$log"
    return "$exit_code"
  fi
}
