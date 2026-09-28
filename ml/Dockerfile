# PhotoShare ML sidecar (CLIP semantic search). CPU-only.
FROM python:3.11-slim

WORKDIR /app

# insightface compiles a small C extension during pip install, so the compiler
# has to be present before requirements are installed -- not after.
RUN apt-get update && apt-get install -y --no-install-recommends build-essential \
    && rm -rf /var/lib/apt/lists/*

# CPU-only torch first (avoids the multi-GB CUDA wheel), then the rest.
RUN pip install --no-cache-dir torch --index-url https://download.pytorch.org/whl/cpu
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY app.py .

# Pre-download both models at build time so the first request is fast and the
# container works with no internet at runtime.
RUN python -c "from sentence_transformers import SentenceTransformer; SentenceTransformer('clip-ViT-B-32')"
RUN python -c "\
from insightface.app import FaceAnalysis; \
a = FaceAnalysis(name='buffalo_l', providers=['CPUExecutionProvider'], allowed_modules=['detection','recognition']); \
a.prepare(ctx_id=-1, det_size=(640,640)); \
print('face model ready')"

EXPOSE 8090
# Single worker: the model is loaded per-process and CPU inference is the
# bottleneck anyway.
CMD ["uvicorn", "app:app", "--host", "0.0.0.0", "--port", "8090"]
