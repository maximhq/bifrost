<!-- The pattern we follow here is to keep the changelog for the latest version -->
<!-- Old changelogs are automatically attached to the GitHub releases -->

- chore: version update core to 1.2.13 and framework to 1.1.15
- feat: added headers support for OTel configuration. Value prefixed with env will be fetched from environment variables (env.<ENV_VAR_NAME>)
- feat: emission of OTel resource spans is completely async - this brings down inference overhead to < 1µsecond