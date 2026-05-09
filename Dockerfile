# Build stripe-dev-server AND vendor the upstream stripe-mock binary so the
# resulting container has both — stripe-dev-server auto-spawns stripe-mock
# from $PATH.
FROM golang:1.22-alpine AS builder

WORKDIR /src
COPY go.mod ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" \
    -o /out/stripe-dev-server ./cmd/stripe-dev-server

# Vendor stripe-mock into the same image.
RUN CGO_ENABLED=0 GOBIN=/out go install github.com/stripe/stripe-mock@latest

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/stripe-dev-server /usr/local/bin/stripe-dev-server
COPY --from=builder /out/stripe-mock        /usr/local/bin/stripe-mock
EXPOSE 12111 12112 12113
ENV PROXY_ADDR=0.0.0.0:12112 \
    UI_ADDR=0.0.0.0:12113 \
    STRIPE_MOCK_ADDR=127.0.0.1:12111 \
    STRIPE_MOCK_BIN=/usr/local/bin/stripe-mock
ENTRYPOINT ["/usr/local/bin/stripe-dev-server"]
