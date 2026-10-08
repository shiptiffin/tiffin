# ShipTiffin's control plane worker (the `worker` app of the `provisioner` project).
# Built by the box with the repository's top as the context.
FROM golang:1.27.2-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/tiffin-provisioner ./cmd/tiffin-provisioner
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/tiffin-provisioner ./cmd/tiffin-provisioner

FROM debian:bookworm-slim
# openssh-client: the setup connects to new servers over SSH (and only during setup).
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates openssh-client \
  && rm -rf /var/lib/apt/lists/* \
  && useradd --system --create-home --home-dir /home/provisioner provisioner
COPY --from=build /out/tiffin-provisioner /usr/local/bin/tiffin-provisioner
USER provisioner
ENV HOME=/home/provisioner
CMD ["/usr/local/bin/tiffin-provisioner"]
