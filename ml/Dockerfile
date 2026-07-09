# PhotoShare ML sidecar (CLIP semantic search). CPU-only.
FROM python:3.11-slim

WORKDIR /app

# insightface pulls in OpenCV, whose shared libs aren't in the slim image
# (import fails with "libxcb.so.1: cannot open shared object file" and friends).
# Install the runtime X/GL libraries OpenCV links against. CLIP-only builds
# don't need these, but they're small and harmless.
RUN apt-get update && apt-get install -y --no-install-recommends \
        libgl1 libglib2.0-0 libxcb1 libsm6 libxext6 libxrender1 \
        libgomp1 \
    && rm -rf /var/lib/apt/lists/*

# CPU-only torch first (avoids the multi-GB CUDA wheel), then the rest.
RUN pip install --no-cache-dir torch --index-url https://download.pytorch.org/whl/cpu
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY app.py .

# Pre-download the CLIP model at build time so the first request is fast and
# the container works with no internet at runtime.
RUN python -c "from sentence_transformers import SentenceTransformer; SentenceTransformer('clip-ViT-B-32')"

# Pre-download the small face model pack too (only used if faces are enabled,
# but baking it in keeps the runtime offline-capable and the first request fast).
RUN python -c "from insightface.app import FaceAnalysis; FaceAnalysis(name='buffalo_s', allowed_modules=['detection','recognition']).prepare(ctx_id=-1, det_size=(320,320))"

EXPOSE 8090
# Single worker: the model is loaded per-process and CPU inference is the
# bottleneck anyway.
CMD ["uvicorn", "app:app", "--host", "0.0.0.0", "--port", "8090"]
