#!/usr/bin/env bash

# Pure Compose/runtime contract helpers shared by preflight, switch, finalize,
# rollback, and coordinated recovery.  Keep network-mode decisions here so a
# host published port is never confused with the port used inside a bridge
# container.

# The runtime image starts as root only long enough to repair /app/data and
# then execs the application as the non-root sub2api user. Release scripts run
# as host root, so a plain `printf > marker && chmod 600` creates a root-owned
# file that PID 1 cannot read. Resolve the actual PID 1 identity and publish
# the marker atomically with matching ownership.
RELEASE_ACTIVATION_MARKER_FAILURE_REASON=unknown
write_release_activation_marker() {
  local container=${1:?container is required}
  local instance_id=${2:?instance ID is required}
  RELEASE_ACTIVATION_MARKER_FAILURE_REASON=unknown
  if ! [[ $container =~ ^[a-zA-Z0-9_.-]{1,100}$ ]]; then RELEASE_ACTIVATION_MARKER_FAILURE_REASON=invalid_container; return 1; fi
  if ! [[ $instance_id =~ ^[a-zA-Z0-9_.-]{1,128}$ ]]; then RELEASE_ACTIVATION_MARKER_FAILURE_REASON=invalid_instance; return 1; fi
  docker inspect "$container" >/dev/null 2>&1 || { RELEASE_ACTIVATION_MARKER_FAILURE_REASON=container_inspect; return 1; }
  local security_options
  if ! security_options=$(docker info --format '{{json .SecurityOptions}}'); then RELEASE_ACTIVATION_MARKER_FAILURE_REASON=docker_security_info; return 1; fi
  if [[ $security_options == *'name=userns'* || $security_options == *'name=rootless'* ]]; then RELEASE_ACTIVATION_MARKER_FAILURE_REASON=unsupported_user_namespace; return 1; fi

  local activation_host_dir runtime_uid runtime_gid marker_tmp marker_path
  if ! activation_host_dir=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/app/data"}}{{.Source}}{{end}}{{end}}' "$container"); then RELEASE_ACTIVATION_MARKER_FAILURE_REASON=data_mount_inspect; return 1; fi
  if ! [[ -n $activation_host_dir && -d $activation_host_dir && ! -L $activation_host_dir ]]; then RELEASE_ACTIVATION_MARKER_FAILURE_REASON=data_mount_invalid; return 1; fi
  if ! runtime_uid=$(docker exec "$container" sh -c 'set -- $(grep "^Uid:" /proc/1/status); printf "%s\n" "$5"'); then RELEASE_ACTIVATION_MARKER_FAILURE_REASON=runtime_uid_read; return 1; fi
  if ! runtime_gid=$(docker exec "$container" sh -c 'set -- $(grep "^Gid:" /proc/1/status); printf "%s\n" "$5"'); then RELEASE_ACTIVATION_MARKER_FAILURE_REASON=runtime_gid_read; return 1; fi
  if ! [[ $runtime_uid =~ ^[0-9]+$ && $runtime_gid =~ ^[0-9]+$ ]]; then RELEASE_ACTIVATION_MARKER_FAILURE_REASON=runtime_identity_invalid; return 1; fi

  marker_path="$activation_host_dir/.sub2api-active-instance"
  if ! marker_tmp=$(mktemp "$activation_host_dir/.sub2api-active-instance.XXXXXXXX"); then RELEASE_ACTIVATION_MARKER_FAILURE_REASON=marker_temp_create; return 1; fi
  if ! printf '%s\n' "$instance_id" > "$marker_tmp" ||
     ! chown "$runtime_uid:$runtime_gid" "$marker_tmp" ||
     ! chmod 600 "$marker_tmp" ||
     [[ $(stat -c '%u:%g:%a:%h' "$marker_tmp") != "$runtime_uid:$runtime_gid:600:1" ]] ||
     ! mv -T -- "$marker_tmp" "$marker_path"; then
    rm -f -- "$marker_tmp"
    RELEASE_ACTIVATION_MARKER_FAILURE_REASON=marker_publish
    return 1
  fi
  if ! [[ ! -L $marker_path ]]; then RELEASE_ACTIVATION_MARKER_FAILURE_REASON=marker_symlink; return 1; fi
  if ! [[ $(stat -c '%u:%g:%a:%h' "$marker_path") == "$runtime_uid:$runtime_gid:600:1" ]]; then RELEASE_ACTIVATION_MARKER_FAILURE_REASON=marker_metadata; return 1; fi
  if ! [[ $(<"$marker_path") == "$instance_id" ]]; then RELEASE_ACTIVATION_MARKER_FAILURE_REASON=marker_content; return 1; fi
}

# Keep human-oriented Docker/Compose progress out of the structured stdout
# contract while retaining the complete original output in the release's
# root-only production log. Non-production integration tests may omit the raw
# log and receive the same quiet command behavior.
run_release_logged_command() {
  if [[ -n ${SUB2API_RELEASE_RAW_LOG:-} ]]; then
    [[ $SUB2API_RELEASE_RAW_LOG == /opt/sub2api/releases/*/logs/production.raw.log ]] || return 1
    [[ -f $SUB2API_RELEASE_RAW_LOG && ! -L $SUB2API_RELEASE_RAW_LOG ]] || return 1
    [[ $(stat -c '%U:%G:%a:%h' "$SUB2API_RELEASE_RAW_LOG") == root:root:600:1 ]] || return 1
    "$@" >> "$SUB2API_RELEASE_RAW_LOG" 2>&1
  else
    "$@" >/dev/null 2>&1
  fi
}

# curl writes response headers with CRLF line endings.  Normalize line endings,
# header-name casing, and optional value whitespace before comparing the single
# expected health header.  This helper is also consumed directly by the VM
# validator, which sources this contract without loading the production context.
assert_http_header_equals() {
  local headers=${1:?headers file is required}
  local name=${2:?header name is required}
  local expected=${3:?expected header value is required}
  [[ -f $headers && ! -L $headers ]]
  [[ $name =~ ^[A-Za-z0-9-]+$ ]]
  [[ $expected =~ ^[A-Za-z0-9_.-]{1,128}$ ]]
  local actual
  actual=$(tr -d '\r' < "$headers" | awk -F: -v name="$name" '
    tolower($1) == tolower(name) {
      sub(/^[^:]*:[[:space:]]*/, "")
      gsub(/^[[:space:]]+|[[:space:]]+$/, "")
      print
    }
  ')
  [[ $(printf '%s\n' "$actual" | grep -c .) == 1 ]]
  [[ $actual == "$expected" ]]
}

sub2api_compose_network_mode() {
  local compose_json=${1:?compose json is required}
  local host_port=${2:?host port is required}
  [[ $host_port == 18080 || $host_port == 18081 ]]
  jq -er --arg port "$host_port" '
    .services.sub2api as $service |
    if (
      $service.network_mode == "host" and
      $service.environment.SERVER_HOST == "127.0.0.1" and
      ($service.environment.SERVER_PORT | tostring) == $port
    ) then
      "host"
    elif (
      ($service.network_mode // "") != "host" and
      $service.environment.SERVER_HOST == "0.0.0.0" and
      ($service.environment.SERVER_PORT | tostring) == "8080" and
      (($service.ports // []) | [
        .[] |
        select(
          (.target | tostring) == "8080" and
          (.published | tostring) == $port and
          .host_ip == "127.0.0.1"
        )
      ] | length) == 1 and
      (($service.ports // []) | [ .[] | select((.target | tostring) == "8080") ] | length) == 1
    ) then
      "bridge"
    else
      error("unsupported sub2api network contract")
    end
  ' <<<"$compose_json"
}

sub2api_healthcheck_url() {
  local network_mode=${1:?network mode is required}
  local host_port=${2:?host port is required}
  [[ $host_port == 18080 || $host_port == 18081 ]]
  case "$network_mode" in
    host) printf 'http://127.0.0.1:%s/health\n' "$host_port" ;;
    bridge) printf 'http://127.0.0.1:8080/health\n' ;;
    *) return 1 ;;
  esac
}

assert_sub2api_healthcheck_contract() {
  local compose_json=${1:?compose json is required}
  local network_mode=${2:?network mode is required}
  local host_port=${3:?host port is required}
  local policy=${4:-strict}
  [[ $policy == strict || $policy == active_compat ]]
  local expected_url
  expected_url=$(sub2api_healthcheck_url "$network_mode" "$host_port")
  if [[ $network_mode == bridge ]]; then
    jq -e --arg expected "$expected_url" --arg policy "$policy" '
      .services.sub2api.healthcheck.test == ["CMD", "wget", "-q", "-T", "5", "-O", "/dev/null", $expected] or
      .services.sub2api.healthcheck.test == ["CMD", "wget", "-q", "-T", "5", "-O", "/dev/null", "http://localhost:8080/health"] or
      (
        $policy == "active_compat" and
        (
          .services.sub2api.healthcheck.test == ["CMD", "wget", "-q", "--spider", $expected] or
          .services.sub2api.healthcheck.test == ["CMD", "wget", "-q", "--spider", "http://localhost:8080/health"]
        )
      )
    ' <<<"$compose_json" >/dev/null
  else
    jq -e --arg expected "$expected_url" --arg policy "$policy" '
      .services.sub2api.healthcheck.test == ["CMD", "wget", "-q", "-T", "5", "-O", "/dev/null", $expected] or
      ($policy == "active_compat" and .services.sub2api.healthcheck.test == ["CMD", "wget", "-q", "--spider", $expected])
    ' <<<"$compose_json" >/dev/null
  fi
}

assert_sub2api_runtime_contract() {
  local container=${1:?container is required}
  local expected_image=${2:?expected image is required}
  local network_mode=${3:?network mode is required}
  local host_port=${4:?host port is required}
  local policy=${5:-strict}
  [[ $policy == strict || $policy == active_compat ]]
  local expected_host expected_internal_port expected_health_url inspect_json
  case "$network_mode" in
    host)
      expected_host=127.0.0.1
      expected_internal_port=$host_port
      ;;
    bridge)
      expected_host=0.0.0.0
      expected_internal_port=8080
      ;;
    *) return 1 ;;
  esac
  expected_health_url=$(sub2api_healthcheck_url "$network_mode" "$host_port")
  inspect_json=$(docker inspect "$container")
  jq -e \
    --arg image "$expected_image" \
    --arg mode "$network_mode" \
    --arg host "$expected_host" \
    --arg internal_port "$expected_internal_port" \
    --arg host_port "$host_port" \
    --arg health_url "$expected_health_url" \
    --arg policy "$policy" '
      .[0] as $container |
      ($container.Image == $image) and
      ([ $container.Config.Env[] | select(startswith("SERVER_HOST=")) ] == [("SERVER_HOST=" + $host)]) and
      ([ $container.Config.Env[] | select(startswith("SERVER_PORT=")) ] == [("SERVER_PORT=" + $internal_port)]) and
      (
        $container.Config.Healthcheck.Test == ["CMD", "wget", "-q", "-T", "5", "-O", "/dev/null", $health_url] or
        (
          $mode == "bridge" and
          $container.Config.Healthcheck.Test == ["CMD", "wget", "-q", "-T", "5", "-O", "/dev/null", "http://localhost:8080/health"]
        ) or
        (
          $policy == "active_compat" and
          (
            $container.Config.Healthcheck.Test == ["CMD", "wget", "-q", "--spider", $health_url] or
            (
              $mode == "bridge" and
              $container.Config.Healthcheck.Test == ["CMD", "wget", "-q", "--spider", "http://localhost:8080/health"]
            )
          )
        )
      ) and
      if $mode == "host" then
        $container.HostConfig.NetworkMode == "host"
      else
        $container.HostConfig.NetworkMode != "host" and
        (($container.NetworkSettings.Ports["8080/tcp"] // []) | length) == 1 and
        $container.NetworkSettings.Ports["8080/tcp"][0].HostIp == "127.0.0.1" and
        $container.NetworkSettings.Ports["8080/tcp"][0].HostPort == $host_port
      end
    ' <<<"$inspect_json" >/dev/null
}

write_release_active_override() {
  local output=${1:?output path is required}
  local image=${2:?image is required}
  local instance_id=${3-}
  local host_port=${4:?host port is required}
  local network_mode=${5:?network mode is required}
  local health_url
  health_url=$(sub2api_healthcheck_url "$network_mode" "$host_port")
  {
    printf 'services:\n  sub2api:\n    image: %s\n    container_name: sub2api\n' "$image"
    if [[ $network_mode == host || -n $instance_id ]]; then
      printf '    environment:\n'
      if [[ $network_mode == host ]]; then
        printf '      SERVER_HOST: 127.0.0.1\n      SERVER_PORT: "%s"\n' "$host_port"
      fi
      if [[ -n $instance_id ]]; then
        printf '      SUB2API_INSTANCE_ID: %s\n' "$instance_id"
        printf '      SUB2API_BACKGROUND_ACTIVATION_FILE: /app/data/.sub2api-active-instance\n'
      fi
    fi
    printf '    healthcheck:\n'
    printf '      test: ["CMD", "wget", "-q", "-T", "5", "-O", "/dev/null", "%s"]\n' "$health_url"
  } > "$output"
}
