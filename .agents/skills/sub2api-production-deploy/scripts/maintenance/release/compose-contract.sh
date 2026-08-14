#!/usr/bin/env bash

# Pure Compose/runtime contract helpers shared by preflight, switch, finalize,
# rollback, and coordinated recovery.  Keep network-mode decisions here so a
# host published port is never confused with the port used inside a bridge
# container.

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
  local expected_url
  expected_url=$(sub2api_healthcheck_url "$network_mode" "$host_port")
  if [[ $network_mode == bridge ]]; then
    jq -e --arg expected "$expected_url" '
      .services.sub2api.healthcheck.test == ["CMD", "wget", "-q", "-T", "5", "-O", "/dev/null", $expected] or
      .services.sub2api.healthcheck.test == ["CMD", "wget", "-q", "-T", "5", "-O", "/dev/null", "http://localhost:8080/health"]
    ' <<<"$compose_json" >/dev/null
  else
    jq -e --arg expected "$expected_url" '
      .services.sub2api.healthcheck.test == ["CMD", "wget", "-q", "-T", "5", "-O", "/dev/null", $expected]
    ' <<<"$compose_json" >/dev/null
  fi
}

assert_sub2api_runtime_contract() {
  local container=${1:?container is required}
  local expected_image=${2:?expected image is required}
  local network_mode=${3:?network mode is required}
  local host_port=${4:?host port is required}
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
    --arg health_url "$expected_health_url" '
      .[0] as $container |
      ($container.Image == $image) and
      ([ $container.Config.Env[] | select(startswith("SERVER_HOST=")) ] == [("SERVER_HOST=" + $host)]) and
      ([ $container.Config.Env[] | select(startswith("SERVER_PORT=")) ] == [("SERVER_PORT=" + $internal_port)]) and
      (
        $container.Config.Healthcheck.Test == ["CMD", "wget", "-q", "-T", "5", "-O", "/dev/null", $health_url] or
        (
          $mode == "bridge" and
          $container.Config.Healthcheck.Test == ["CMD", "wget", "-q", "-T", "5", "-O", "/dev/null", "http://localhost:8080/health"]
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
