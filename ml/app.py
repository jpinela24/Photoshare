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
