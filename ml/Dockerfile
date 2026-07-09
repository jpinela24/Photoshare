# PhotoShare ML sidecar (CLIP semantic search). CPU-only.
FROM python:3.11-slim

WORKDIR /app

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
