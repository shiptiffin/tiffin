# ShipTiffin's control plane worker (the `cloud` app of the `website` project).
# Built by the box with the repository's top as the context.
FROM golang:1.27.2-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/tiffin-cloud ./cmd/tiffin-cloud
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/tiffin-cloud ./cmd/tiffin-cloud

FROM debian:bookworm-slim
# openssh-client: the setup connects to new servers over SSH (and only during setup).
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates openssh-client \
  && rm -rf /var/lib/apt/lists/* \
  && useradd --system --create-home --home-dir /home/cloud cloud
COPY --from=build /out/tiffin-cloud /usr/local/bin/tiffin-cloud
USER cloud
ENV HOME=/home/cloud
CMD ["/usr/local/bin/tiffin-cloud"]
