FROM golang:1.26-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /bin/node ./cmd/node

# ---

FROM scratch

COPY --from=builder /bin/node /bin/node

ENTRYPOINT ["/bin/node"]
