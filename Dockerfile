ARG TARGETARCH

FROM alpine:latest
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app

ARG TARGETARCH
# Copy the pre-built binary based on the target architecture
COPY dist/linux_${TARGETARCH}/streamnzb .

EXPOSE 7000
CMD ["./streamnzb"]
