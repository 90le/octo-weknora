# Offline Pro-PPTX Sandbox Extension Image
# Extends the existing WeKnora sandbox runtime (Python 3.12 + Node 20 + office libs)
# with headless Chromium (Playwright), pptxgenjs, offline ECharts, Noto CJK fonts,
# matplotlib/seaborn, and LibreOffice Impress + poppler-utils for Visual QA.
#
# Built in a network-enabled build environment; runs completely offline (network_mode: none).

ARG BASE_IMAGE=local/octo-sandbox:documents-651dadee
FROM ${BASE_IMAGE}

USER root
ENV DEBIAN_FRONTEND=noninteractive
ENV LANG=C.UTF-8
ENV LC_ALL=C.UTF-8
ENV PLAYWRIGHT_BROWSERS_PATH=/opt/pw-browsers
ENV PRO_PPTX_RUNTIME=/opt/pro-pptx-runtime
ENV NODE_PATH=/opt/pro-pptx-runtime/node_modules

ARG APT_MIRROR=mirrors.aliyun.com
ARG NPM_REGISTRY=https://registry.npmmirror.com
ARG PW_DOWNLOAD_HOST=https://npmmirror.com/mirrors/playwright
ARG PIP_INDEX_URL=https://pypi.tuna.tsinghua.edu.cn/simple

# 1. CJK fonts, LibreOffice Impress (for real PPTX visual QA), poppler-utils (pdftoppm),
#    and Chromium headless system shared libraries.
RUN set -eux; \
    if [ -f /etc/apt/sources.list.d/debian.sources ]; then \
      sed -i "s|deb.debian.org|${APT_MIRROR}|g; s|security.debian.org|${APT_MIRROR}|g" /etc/apt/sources.list.d/debian.sources; \
    elif [ -f /etc/apt/sources.list ]; then \
      sed -i "s|deb.debian.org|${APT_MIRROR}|g; s|security.debian.org|${APT_MIRROR}|g" /etc/apt/sources.list; \
    fi; \
    apt-get update && apt-get install -y --no-install-recommends \
    fonts-noto-cjk \
    fonts-dejavu-core \
    fontconfig \
    libreoffice-impress \
    libreoffice-common \
    poppler-utils \
    libnss3 \
    libnspr4 \
    libatk1.0-0 \
    libatk-bridge2.0-0 \
    libcups2 \
    libdrm2 \
    libdbus-1-3 \
    libxkbcommon0 \
    libxcomposite1 \
    libxdamage1 \
    libxfixes3 \
    libxrandr2 \
    libgbm1 \
    libasound2 \
    libpango-1.0-0 \
    libcairo2 \
    libxshmfence1 \
    libx11-xcb1 \
    && fc-cache -fv \
    && rm -rf /var/lib/apt/lists/* /var/cache/apt/*

# 2. Offline Node runtime for HTML/CSS -> Chromium DOM measurement -> native PPTX compilation
RUN mkdir -p "${PRO_PPTX_RUNTIME}" "${PLAYWRIGHT_BROWSERS_PATH}" \
    && cd "${PRO_PPTX_RUNTIME}" \
    && npm init -y >/dev/null 2>&1 \
    && npm install --omit=dev --no-audit --no-fund --registry="${NPM_REGISTRY}" \
        playwright@1.52.0 \
        pptxgenjs@3.12.0 \
        echarts@5.6.0 \
    && PLAYWRIGHT_DOWNLOAD_HOST="${PW_DOWNLOAD_HOST}" npx playwright install chromium \
    && chmod -R a+rX "${PRO_PPTX_RUNTIME}" "${PLAYWRIGHT_BROWSERS_PATH}"

# 3. Python statistical charting libraries (office libs already present in base image)
RUN python3 -m pip install --no-cache-dir -i "${PIP_INDEX_URL}" \
    matplotlib \
    seaborn \
    numpy

# Verify all key offline components at build time
RUN node -e "require('playwright'); require('pptxgenjs'); require('echarts'); console.log('NODE_PRO_PPTX_OK')" \
    && python3 -c "import matplotlib, seaborn, numpy, PIL, pptx, docx, openpyxl, reportlab; print('PY_PRO_PPTX_OK')" \
    && soffice --version \
    && pdftoppm -v

WORKDIR /workspace
USER root
