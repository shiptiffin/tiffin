"""Tables. Change one, then write a migration in migrations/versions/."""

from datetime import datetime

from sqlalchemy import CheckConstraint, DateTime, Identity, func
from sqlalchemy.orm import DeclarativeBase, Mapped, mapped_column


class Base(DeclarativeBase):
    type_annotation_map = {datetime: DateTime(timezone=True)}  # timestamptz


class Note(Base):
    __tablename__ = "notes"
    __table_args__ = (CheckConstraint("length(text) between 1 and 2000", name="notes_text_length"),)

    id: Mapped[int] = mapped_column(Identity(), primary_key=True)
    text: Mapped[str]
    done: Mapped[bool] = mapped_column(default=False, server_default="false")
    created_at: Mapped[datetime] = mapped_column(server_default=func.now())
    updated_at: Mapped[datetime] = mapped_column(server_default=func.now())
