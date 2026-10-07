import os

import pytest
from asgi_lifespan import LifespanManager
from httpx import ASGITransport, AsyncClient


@pytest.fixture
def anyio_backend():
    return "asyncio"


@pytest.fixture(scope="session")
def migrated():
    """Migrates DATABASE_URL (point it at a scratch database); skips without one."""
    if not os.environ.get("DATABASE_URL"):
        pytest.skip("set DATABASE_URL to a scratch Postgres database to run the database tests")
    from alembic import command
    from alembic.config import Config

    command.upgrade(Config(toml_file="pyproject.toml"), "head")


@pytest.fixture
async def client():
    from app.main import app

    async with LifespanManager(app) as manager:
        async with AsyncClient(transport=ASGITransport(app=manager.app), base_url="http://test") as c:
            yield c
