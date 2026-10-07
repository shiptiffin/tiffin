"""What the API takes and returns (they also make the OpenAPI docs at /docs)."""

from datetime import datetime
from typing import Annotated, Self

from pydantic import BaseModel, ConfigDict, Field, StringConstraints, model_validator

Text = Annotated[str, StringConstraints(strip_whitespace=True, min_length=1, max_length=2000)]


class NoteIn(BaseModel):
    text: Text = Field(examples=["Buy oat milk"])


class NotePatch(BaseModel):
    text: Text | None = None
    done: bool | None = None

    @model_validator(mode="after")
    def needs_a_change(self) -> Self:
        if self.text is None and self.done is None:
            raise ValueError("send text and/or done")
        return self


class Note(BaseModel):
    model_config = ConfigDict(from_attributes=True)

    id: int
    text: str
    done: bool
    created_at: datetime
    updated_at: datetime


class Notes(BaseModel):
    notes: list[Note]
