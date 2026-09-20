from __future__ import annotations

import argparse


def register_commands(subparsers: argparse._SubParsersAction) -> None:
    start_parser = subparsers.add_parser("plugin-deploy-start", help="build and start an independent plugin release")
    start_parser.add_argument("--commit", required=True)
    start_parser.set_defaults(handler=lambda args: __import__("release.plugin_supervisor", fromlist=["start"]).start(args))

    follow_start_parser = subparsers.add_parser("plugin-deploy-follow", help="start and interactively follow a plugin release")
    follow_start_parser.add_argument("--commit", required=True)
    follow_start_parser.add_argument("--lang", choices=("zh-CN",), default="zh-CN")
    follow_start_parser.add_argument("--heartbeat", type=int, default=5)
    follow_start_parser.set_defaults(handler=lambda args: __import__("release.plugin_observer", fromlist=["deploy_follow"]).deploy_follow(args))

    authorize_parser = subparsers.add_parser("plugin-authorize", help="resume a plugin release stopped at a legacy write checkpoint")
    authorize_parser.add_argument("release_id")
    authorize_parser.set_defaults(handler=lambda args: __import__("release.plugin_supervisor", fromlist=["authorize"]).authorize(args))

    status_parser = subparsers.add_parser("plugin-status", help="show an allowlisted plugin release status")
    status_parser.add_argument("release_id")
    status_parser.set_defaults(handler=lambda args: __import__("release.plugin_supervisor", fromlist=["status"]).status(args))

    follow_parser = subparsers.add_parser("plugin-follow", help="follow an existing plugin release")
    follow_parser.add_argument("release_id")
    follow_parser.add_argument("--lang", choices=("zh-CN",), default="zh-CN")
    follow_parser.add_argument("--heartbeat", type=int, default=5)
    follow_parser.set_defaults(handler=lambda args: __import__("release.plugin_observer", fromlist=["follow"]).follow(args))

    wait_parser = subparsers.add_parser("plugin-wait", help="wait for a plugin release checkpoint")
    wait_parser.add_argument("release_id")
    wait_parser.add_argument("--timeout", type=int, default=0)
    wait_parser.set_defaults(handler=lambda args: __import__("release.plugin_supervisor", fromlist=["wait"]).wait(args))

    verify_parser = subparsers.add_parser("plugin-verify-result", help="verify terminal plugin release evidence")
    verify_parser.add_argument("release_id")
    verify_parser.set_defaults(handler=lambda args: __import__("release.plugin_supervisor", fromlist=["verify_result"]).verify_result(args))

    rollback_parser = subparsers.add_parser("plugin-rollback-start", help="rollback using an archived trusted plugin package")
    rollback_parser.add_argument("release_id")
    rollback_parser.set_defaults(handler=lambda args: __import__("release.plugin_supervisor", fromlist=["rollback_start"]).rollback_start(args))

    worker_parser = subparsers.add_parser("_plugin-deploy-worker", help=argparse.SUPPRESS)
    worker_parser.add_argument("--release-id", required=True)
    worker_parser.add_argument("--commit", required=True)
    worker_parser.set_defaults(handler=lambda args: __import__("release.plugin_supervisor", fromlist=["worker"]).worker(args))
