FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o bread_orders .

FROM alpine:3.20
WORKDIR /app
COPY --from=builder /app/bread_orders ./
COPY templates/ templates/
RUN mkdir -p data
EXPOSE 8080
CMD ["./bread_orders"]
