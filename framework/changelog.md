[fix]: normalize virtual key active defaults and repair stored NULL states safely during concurrent sync and rolling upgrades [@Constantine3](https://github.com/Constantine3)
- chore: upgraded core to v1.15.2
- [test]: regression tests pin advisor-pass billing on the Anthropic passthrough stream, with no repeated per-request fee, fast mode and US data residency carried to the advisor pass, and no advisor billing on semantic-cache hits (from #8783) [@tomatomptt](https://github.com/tomatomptt)
- feat: oauth2_server_config.gateways.claude_code setting and device-code columns on oauth2_authorize_requests for Claude Code gateway sign-in (#7963) [@jimseiwert](https://github.com/jimseiwert)
- [feat]: MachGen video jobs are settled and priced by the video job sweeper
