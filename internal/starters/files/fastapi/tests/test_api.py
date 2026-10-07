import pytest

pytestmark = pytest.mark.anyio


async def test_health(client):
    res = await client.get("/healthz")
    assert res.status_code == 200 and res.text == "ok"


async def test_openapi_lists_the_routes(client):
    paths = (await client.get("/openapi.json")).json()["paths"]
    assert {"/notes", "/notes/{id}", "/me"} <= paths.keys()
    assert (await client.get("/docs")).status_code == 200


async def test_validation(client):
    assert (await client.post("/notes", json={"text": "   "})).status_code == 422
    assert (await client.get("/notes/0")).status_code == 422


async def test_notes(client, migrated):
    res = await client.post("/notes", json={"text": "  write tests  "})
    assert res.status_code == 201, res.text
    note = res.json()
    assert note["text"] == "write tests" and note["done"] is False

    res = await client.patch(f"/notes/{note['id']}", json={"done": True})
    assert res.status_code == 200 and res.json()["done"] is True
    assert (await client.patch(f"/notes/{note['id']}", json={})).status_code == 422

    listed = (await client.get("/notes", params={"done": "true"})).json()["notes"]
    assert note["id"] in [n["id"] for n in listed]

    assert (await client.delete(f"/notes/{note['id']}")).status_code == 204
    assert (await client.get(f"/notes/{note['id']}")).status_code == 404


async def test_me_without_sign_in(client, monkeypatch):
    monkeypatch.delenv("TIFFIN_AUTH_INTERNAL_URL", raising=False)
    assert (await client.get("/me")).status_code == 404
