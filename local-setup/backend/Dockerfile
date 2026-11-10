FROM golang:1.26.8-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY local-setup/backend ./local-setup/backend
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/backend ./local-setup/backend

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/backend /backend
EXPOSE 8080
ENTRYPOINT ["/backend"]
