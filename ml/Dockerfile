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

EXPOSE 8090
# Single worker: the model is loaded per-process and CPU inference is the
# bottleneck anyway.
CMD ["uvicorn", "app:app", "--host", "0.0.0.0", "--port", "8090"]
