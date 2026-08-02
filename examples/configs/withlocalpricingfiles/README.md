# Local pricing & model parameters files (air-gapped / behind a proxy)

This config makes Bifrost load its pricing datasheet and model parameters
datasheet from local files instead of fetching them from `getbifrost.ai`. Use
it when the host has no outbound internet access, or when an HTTP proxy blocks
DNS resolution of `getbifrost.ai` (see issue #4305).

Both `pricing_url` and `model_parameters_url` use the `file://` scheme. When the
scheme is `file`, Bifrost reads the path directly off disk and never performs
external URL validation or hostname resolution.

The paths are **relative** (`file://../../examples/configs/withlocalpricingfiles/pricing.json`),
resolved against the working directory the same way the sqlite config store's
`path` is - matching the convention used by the other example configs, which run
from the repo's `transports` directory. This keeps the datasheets in the example
folder so it is self-contained - no need to copy them to an absolute system
path. Absolute `file:///abs/path.json` URLs also work.

## Files

- `config.json` - the Bifrost config, pointing both URLs at the sibling `*.json` files.
- `pricing.json` - a small sample pricing datasheet (real entries).
- `model-parameters.json` - a small sample model parameters datasheet (real entries).

The two sample datasheets contain a handful of models so the example boots
quickly. For a full catalog, download the complete datasheets and replace them:

```bash
curl -fsSL https://getbifrost.ai/datasheet -o pricing.json
curl -fsSL https://getbifrost.ai/datasheet/model-parameters -o model-parameters.json
```

## Run with Docker

The relative paths in `config.json` assume the repo layout, so for Docker it is
simplest to use absolute `file://` URLs and mount the files at those paths.
Either edit the two URLs to `file:///app/data/pricing.json` and
`file:///app/data/model-parameters.json`, or mount them where the URLs point:

```bash
docker run -p 8080:8080 \
  -e OPENAI_API_KEY=sk-... \
  -v "$(pwd)/config.json:/app/data/config.json" \
  -v "$(pwd)/pricing.json:/app/data/pricing.json" \
  -v "$(pwd)/model-parameters.json:/app/data/model-parameters.json" \
  maximhq/bifrost
```

## Run locally

Run from the repo's `transports` directory (the same place the other example
configs are run from) so the `../../examples/configs/...` relative paths resolve.
Alternatively, edit `config.json` to point both URLs at an absolute location,
e.g. `"file:///absolute/path/to/pricing.json"`.
