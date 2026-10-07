"""Alembic's runner. `alembic upgrade head` is the app's release command: the
box runs it once per deploy, before the new version takes traffic, with
DIRECT_DATABASE_URL straight to Postgres (migrations need a whole session)."""

import asyncio
import logging

from alembic import context
from sqlalchemy import pool
from sqlalchemy.ext.asyncio import create_async_engine

from app.db import database_url
from app.models import Base

# Say what runs in the deploy log ("Running upgrade  -> 0001, Create notes.").
logging.basicConfig(level=logging.INFO, format="%(levelname)-5.5s [%(name)s] %(message)s")
logging.getLogger("sqlalchemy.engine").setLevel(logging.WARNING)

target_metadata = Base.metadata  # `alembic revision --autogenerate` compares against it


def run(connection) -> None:
    context.configure(connection=connection, target_metadata=target_metadata, transaction_per_migration=True)
    with context.begin_transaction():
        context.run_migrations()


async def online() -> None:
    engine = create_async_engine(database_url("DIRECT_DATABASE_URL"), poolclass=pool.NullPool)
    async with engine.connect() as connection:
        await connection.run_sync(run)
    await engine.dispose()


if context.is_offline_mode():
    context.configure(url=database_url("DIRECT_DATABASE_URL"), target_metadata=target_metadata, literal_binds=True)
    with context.begin_transaction():
        context.run_migrations()
else:
    asyncio.run(online())
