# Start from a lightweight golang base image
FROM golang:1.20-alpine AS builder

# Set the working directory
WORKDIR /app

# Copy the source code into the container
COPY . .

# Initialize the Go module
RUN go mod init process-exporter-lite

# Ensure dependencies are correct
RUN go mod tidy

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o metrics .

# Start a new stage from scratch
FROM alpine:latest  

RUN apk --no-cache add ca-certificates

WORKDIR /root/

# Copy the pre-built binary file from the previous stage
COPY --from=builder /app/metrics .

# Command to run the executable
CMD ["./metrics"]
