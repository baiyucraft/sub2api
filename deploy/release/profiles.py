from __future__ import annotations


PROFILES = {
    "182": {
        "name": "182",
        "origin": "https://github.com/baiyucraft/sub2api.git",
        "version": "0.1.153-baiyu",
        "migrations": ["182_upstream_actual_rate_multiplier.sql"],
        "gate_ttl_seconds": 86400,
        "vm_identity": "sub2api-dev",
        "vm_source": "/opt/sub2api-src",
        "vm_deploy": "/opt/sub2api-deploy",
        "vm_data": "/opt/sub2api-deploy/data-dev",
        "rack_source": "/opt/sub2api-src",
        "rack_deploy": "/opt/sub2api",
        "public_domain": "sub.baiyuapi.xyz",
        "rack_public_ip": "173.254.217.135",
        "dmit_public_ip": "179.255.148.240",
        "production_health_port": 18080,
        "minimum_rack_free_bytes": 10737418240,
        "minimum_backup_free_bytes": 5368709120,
        "minimum_free_after_bytes": 2147483648,
    }
}


def get_profile(name: str) -> dict:
    try:
        return dict(PROFILES[name])
    except KeyError as error:
        raise ValueError(f"unknown release profile: {name}") from error
