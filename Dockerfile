FROM golang:1.22.4-bullseye as builder

# Create and change to the app directory.
WORKDIR /app

# Retrieve application dependencies.
# This allows the container build to reuse cached dependencies.
# Expecting to copy go.mod and if present go.sum.
COPY go.* ./
RUN go mod download

# Copy local code to the container image.
COPY . ./

# Obtain the current Git commit ID and model version dynamically
ARG COMMIT_ID
ARG MODEL_VERSION

# Build the binary.
RUN go build -a -installsuffix cgo -ldflags "-X main.CommitID=${COMMIT_ID} -X main.ModelVersion=${MODEL_VERSION}" -v -o bundler cmd/main.go

# Use the official Debian slim image for a lean production container.
# https://hub.docker.com/_/debian
# https://docs.docker.com/develop/develop-images/multistage-build/#use-multi-stage-builds
FROM debian:buster-slim
RUN set -x && apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y \
  ca-certificates curl && \
  rm -rf /var/lib/apt/lists/*

# Copy the binary to the production image from the builder stage.
COPY --from=builder /app/bundler /app/bundler

EXPOSE 4337

CMD ["/app/bundler"]
