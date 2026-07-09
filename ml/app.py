"""PhotoShare ML sidecar — CLIP embeddings for local semantic search.

A tiny HTTP service PhotoShare's Go backend calls to turn images and text
queries into vectors in the same embedding space (OpenAI CLIP ViT-B/32 via
sentence-transformers). Everything runs locally on CPU — no data leaves the box.

Endpoints:
  GET  /health          -> {"ok": true}
  POST /clip/image      (multipart "file") -> {"embedding": [...512 floats...]}
  POST /clip/text       ({"text": "..."})  -> {"embedding": [...512 floats...]}
"""
import io

from fastapi import FastAPI, File, UploadFile, HTTPException
from pydantic import BaseModel
from PIL import Image
from sentence_transformers import SentenceTransformer

# clip-ViT-B-32 encodes BOTH images and text into the same 512-dim space.
model = SentenceTransformer("clip-ViT-B-32")

app = FastAPI(title="PhotoShare ML")


@app.get("/health")
def health():
    return {"ok": True}


@app.post("/clip/image")
async def clip_image(file: UploadFile = File(...)):
    try:
        data = await file.read()
        img = Image.open(io.BytesIO(data)).convert("RGB")
    except Exception as e:
        raise HTTPException(status_code=400, detail=f"bad image: {e}")
    emb = model.encode(img, normalize_embeddings=True)
    return {"embedding": emb.tolist()}


class TextIn(BaseModel):
    text: str


@app.post("/clip/text")
def clip_text(inp: TextIn):
    # A light prompt template improves CLIP text↔image retrieval.
    emb = model.encode(f"a photo of {inp.text}", normalize_embeddings=True)
    return {"embedding": emb.tolist()}


# ── Faces (Phase 2) ──────────────────────────────────────────────────────────
# Loaded lazily on the first /faces/detect call, so when face recognition is
# turned off this costs nothing — no extra RAM, no model download hit at boot.
# buffalo_s + a 320px detector is deliberately the small/cheap config.
_face_app = None


def _faces():
    global _face_app
    if _face_app is None:
        import numpy as np  # noqa: F401 (ensures numpy present before insightface)
        from insightface.app import FaceAnalysis

        fa = FaceAnalysis(
            name="buffalo_s",
            providers=["CPUExecutionProvider"],
            allowed_modules=["detection", "recognition"],
        )
        fa.prepare(ctx_id=-1, det_size=(320, 320))  # ctx_id=-1 → CPU
        _face_app = fa
    return _face_app


@app.post("/faces/detect")
async def faces_detect(file: UploadFile = File(...)):
    try:
        import numpy as np

        data = await file.read()
        img = Image.open(io.BytesIO(data)).convert("RGB")
    except Exception as e:
        raise HTTPException(status_code=400, detail=f"bad image: {e}")
    arr = np.asarray(img)[:, :, ::-1]  # PIL RGB → BGR that insightface expects
    faces = []
    for f in _faces().get(arr):
        x1, y1, x2, y2 = [float(v) for v in f.bbox]
        faces.append(
            {
                "box": [x1, y1, x2, y2],
                "score": float(f.det_score),
                "embedding": f.normed_embedding.tolist(),  # already L2-normalized
            }
        )
    return {"faces": faces, "w": img.width, "h": img.height}
