# Embedding adapter

Set `OCTO_EMBEDDING_UPSTREAM` to the full HTTPS URL of the deployment's
multimodal embedding endpoint before starting `python embedding-adapter.py`.
The variable is required: missing, malformed, non-HTTPS, credential-bearing,
or fragment-bearing URLs cause startup to fail. Configure the real endpoint
outside the source tree; do not put credentials in the URL. The adapter does
not follow upstream redirects.

The adapter listens on port 8090. `POST /v1/embeddings` requires a Bearer
credential, which is forwarded to the configured upstream. It accepts model
`qwen3-vl-embedding` with either `input` as a string or a batch of up to 64
non-empty strings, one user message whose content consists of text parts, or
a batch of independent single-user-message conversations in that format.
Input batches are forwarded in groups of ten; returned vectors must have 2560
dimensions. `GET /health` provides a health check.

`sandbox-limit.py` is the admission plugin for the dedicated Octo sandbox VM.
It limits that VM's Docker daemon to four running containers and must not be
installed on the production application's Docker daemon. Size the isolated VM
for its sandbox limits before installing it.

Run the offline tests from this directory:

```text
python -B -m unittest -v test_embedding_adapter
python -B -m unittest -v test_sandbox_limit
```

Tests use a loopback adapter server and mocked upstream responses; they do not
contact the configured upstream.
