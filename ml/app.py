"""PhotoShare ML sidecar — CLIP embeddings and face detection, all local.

A tiny HTTP service PhotoShare's Go backend calls. Everything runs on CPU and
no data leaves the box.

Endpoints:
  GET  /health          -> {"ok": true, "faces": bool}
  POST /clip/image      (multipart "file") -> {"embedding": [...512 floats...]}
  POST /clip/text       ({"text": "..."})  -> {"embedding": [...512 floats...]}
  POST /faces/detect    (multipart "file") -> {"faces": [{box, score, embedding}]}
"""
import io
import os

import numpy as np
from fastapi import FastAPI, File, UploadFile, HTTPException
from pydantic import BaseModel
from PIL import Image, ImageOps
from sentence_transformers import SentenceTransformer

# clip-ViT-B-32 encodes BOTH images and text into the same 512-dim space.
model = SentenceTransformer("clip-ViT-B-32")

# -- Faces --------------------------------------------------------------------
#
# insightface's buffalo_l: SCRFD for detection, ArcFace (w600k_r50) for the
# 512-dim identity embedding. Optional -- if the package or its models are
# missing the sidecar still serves CLIP and reports faces:false, so an existing
# deployment is never broken by this being unavailable.
#
# On what these embeddings can and cannot do: ArcFace is trained mostly on
# adults, and similarity across a large age gap -- especially in childhood --
# drops a long way. PhotoShare therefore clusters tightly and lets one person
# own several clusters rather than trying to match every age to a single face.
# Do not raise the thresholds here hoping to close that gap; it raises false
# positives instead.
_face_app = None
try:
    from insightface.app import FaceAnalysis

    _face_app = FaceAnalysis(
        name="buffalo_l",
        providers=["CPUExecutionProvider"],
        allowed_modules=["detection", "recognition"],  # skip age/gender/landmarks
    )
    # det_size is the detector's working resolution. 640 catches noticeably more
    # small/background faces than the 320 default, at some CPU cost -- worth it
    # for a family library where people often aren't the subject of the frame.
    _face_app.prepare(ctx_id=-1, det_size=(640, 640))
except Exception as _e:  # noqa: BLE001 - any import/model failure disables faces
    print(f"[faces] unavailable: {_e}", flush=True)
    _face_app = None

# A detection below this confidence is noise more often than a face. The whole
# design favours precision over recall: a missed face costs one photo, a wrong
# one pollutes a cluster and erodes trust in every group.
MIN_DET_SCORE = float(os.environ.get("FACE_MIN_SCORE", "0.65"))
# Faces smaller than this (longest side, in ORIGINAL image pixels) carry too
# little detail for a usable identity vector.
MIN_FACE_PX = int(os.environ.get("FACE_MIN_PX", "48"))
# Cap the working size: a 48MP photo costs a lot of CPU for no extra accuracy.
MAX_SIDE = 1600

app = FastAPI(title="PhotoShare ML")


@app.get("/health")
def health():
    return {"ok": True, "faces": _face_app is not None}


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


@app.post("/faces/detect")
async def faces_detect(file: UploadFile = File(...)):
    """Detect faces and return one identity embedding per face.

    Boxes come back in ORIGINAL image pixels even when detection ran on a
    downscaled copy, so the caller can crop straight from the file it has.
    """
    if _face_app is None:
        raise HTTPException(status_code=501, detail="face model not installed")
    try:
        data = await file.read()
        img = Image.open(io.BytesIO(data))
        # Respect EXIF orientation: a sideways face is a missed face.
        img = ImageOps.exif_transpose(img).convert("RGB")
    except Exception as e:
        raise HTTPException(status_code=400, detail=f"bad image: {e}")

    scale = 1.0
    if max(img.size) > MAX_SIDE:
        scale = MAX_SIDE / max(img.size)
        img = img.resize((round(img.width * scale), round(img.height * scale)))

    # insightface expects BGR, like OpenCV.
    arr = np.asarray(img)[:, :, ::-1]
    try:
        found = _face_app.get(arr)
    except Exception as e:  # a corrupt image shouldn't take the service down
        raise HTTPException(status_code=400, detail=f"detection failed: {e}")

    out = []
    for f in found:
        score = float(getattr(f, "det_score", 0.0))
        if score < MIN_DET_SCORE:
            continue
        x1, y1, x2, y2 = (float(v) for v in f.bbox)
        x1, y1, x2, y2 = (v / scale for v in (x1, y1, x2, y2))  # original pixels
        w, h = x2 - x1, y2 - y1
        if max(w, h) < MIN_FACE_PX:
            continue
        emb = f.normed_embedding  # L2-normalised: cosine similarity == dot product
        if emb is None:
            continue
        out.append({
            "box": [max(0, round(x1)), max(0, round(y1)), round(w), round(h)],
            "score": round(score, 4),
            "embedding": [float(v) for v in emb],
        })
    # Biggest first: the subject is usually the largest face, which makes the
    # first crop shown for a cluster a good one.
    out.sort(key=lambda d: d["box"][2] * d["box"][3], reverse=True)
    return {"faces": out}
