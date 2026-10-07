"""A notes API: FastAPI with Postgres on a Tiffin box.

Run it locally with `uv run uvicorn app.main:app --reload` (DATABASE_URL set,
after `uv run alembic upgrade head`); the docs are at /docs.
"""

import logging
import os
from contextlib import asynccontextmanager
from typing import Annotated, Any

from fastapi import Depends, FastAPI, HTTPException, Path, Query, Request, Response
from fastapi.responses import PlainTextResponse
from sqlalchemy import delete, func, select, update
from sqlalchemy.ext.asyncio import AsyncSession
from uvicorn.logging import DefaultFormatter

from . import auth, schemas
from .db import engine, session
from .models import Note

# The app's own log lines look like Uvicorn's ("INFO:     ..."), so the box
# can tell errors and warnings apart in its logs.
_handler = logging.StreamHandler()
_handler.setFormatter(DefaultFormatter("%(levelprefix)s %(message)s", use_colors=False))
log = logging.getLogger("notes")
log.addHandler(_handler)
log.setLevel(logging.INFO)
log.propagate = False


@asynccontextmanager
async def lifespan(app: FastAPI):
    if auth.enabled():
        import httpx  # only when sign-in is on

        app.state.http = httpx.AsyncClient(timeout=5)
    log.info("notes API ready (deploy %s)", os.environ.get("TIFFIN_DEPLOY", "local"))
    yield
    # Uvicorn gets here on SIGTERM, once requests in flight are done.
    if auth.enabled():
        await app.state.http.aclose()
    await engine.dispose()


app = FastAPI(title="Notes API", version="0.1.0", lifespan=lifespan)

DB = Annotated[AsyncSession, Depends(session)]
NoteID = Annotated[int, Path(ge=1, le=2**31 - 1)]


@app.get("/", summary="What this API offers")
async def index(request: Request) -> dict[str, Any]:
    return {
        "name": "notes",
        "deploy": os.environ.get("TIFFIN_DEPLOY", "local"),
        # https://<your host>/docs: behind the box's edge, Uvicorn takes the
        # scheme and client address from its X-Forwarded-* headers.
        "docs": f"{request.base_url}docs",
        "endpoints": {
            "GET /notes": "list notes, newest first (?done=true|false to filter)",
            "POST /notes": 'create one: {"text": "..."}',
            "GET /notes/{id}": "read one",
            "PATCH /notes/{id}": 'change text and/or done: {"text": "...", "done": true}',
            "DELETE /notes/{id}": "delete one",
            "GET /me": "who is signed in (with services.auth)",
        },
    }


# The healthcheck in tiffin.config.ts: cheap, and needs no database.
@app.get("/healthz", response_class=PlainTextResponse, include_in_schema=False)
async def healthz() -> str:
    return "ok"


@app.get("/notes", response_model=schemas.Notes)
async def list_notes(db: DB, done: bool | None = None, limit: Annotated[int, Query(ge=1, le=200)] = 200):
    q = select(Note).order_by(Note.id.desc()).limit(limit)
    if done is not None:
        q = q.where(Note.done == done)
    return {"notes": (await db.scalars(q)).all()}


@app.post("/notes", response_model=schemas.Note, status_code=201)
async def create_note(body: schemas.NoteIn, db: DB):
    note = Note(text=body.text)
    db.add(note)
    await db.commit()
    await db.refresh(note)
    return note


@app.get("/notes/{id}", response_model=schemas.Note, responses={404: {"description": "No such note"}})
async def get_note(id: NoteID, db: DB):
    if note := await db.get(Note, id):
        return note
    raise HTTPException(404, "no such note")


@app.patch("/notes/{id}", response_model=schemas.Note, responses={404: {"description": "No such note"}})
async def update_note(id: NoteID, body: schemas.NotePatch, db: DB):
    changes = body.model_dump(exclude_none=True)
    note = await db.scalar(update(Note).where(Note.id == id).values(**changes, updated_at=func.now()).returning(Note))
    if note is None:
        raise HTTPException(404, "no such note")
    await db.commit()
    return note


@app.delete("/notes/{id}", status_code=204, responses={404: {"description": "No such note"}})
async def delete_note(id: NoteID, db: DB) -> Response:
    deleted = await db.scalar(delete(Note).where(Note.id == id).returning(Note.id))
    if deleted is None:
        raise HTTPException(404, "no such note")
    await db.commit()
    return Response(status_code=204)


@app.get("/me", summary="Who is signed in", responses={401: {"description": "Signed out"}})
async def me(request: Request) -> dict[str, Any]:
    if s := await auth.session(request):
        return s
    raise HTTPException(401, "not signed in")
