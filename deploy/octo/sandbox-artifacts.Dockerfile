ARG BASE_IMAGE=wechatopenai/weknora-sandbox@sha256:5e1cca768f35724a5bdee18e2191d37c0d4a3c78e703a5beaccc3fb4a204a5d9
FROM ${BASE_IMAGE} AS dependencies

COPY sandbox-artifacts-requirements.lock /tmp/requirements.lock
COPY wheels/ /tmp/wheels/
RUN python3 -m pip install --no-cache-dir --no-index --find-links=/tmp/wheels \
    --require-hashes --target=/opt/octo-document-libs -r /tmp/requirements.lock

FROM ${BASE_IMAGE}
COPY --from=dependencies /opt/octo-document-libs/ /usr/local/lib/python3.12/site-packages/
RUN python3 -m pip check
