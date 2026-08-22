ARG TARGETARCH

# Pinned to a release rather than :latest so two builds of the same commit
# produce the same image, and so a broken Alpine release cannot break the build
# with nothing in this repository having changed. Dependabot proposes the bumps.
FROM alpine:3.24
# sqlite is the CLI only — the server itself uses a pure-Go driver and does not
# link against it. It ships so maintenance SQL against /app/data/streamnzb.db
# can be run from inside the container without installing anything first.
RUN apk add --no-cache ca-certificates tzdata sqlite
WORKDIR /app

ARG TARGETARCH
# Copy the pre-built binary based on the target architecture
COPY dist/linux_${TARGETARCH}/streamnzb .

EXPOSE 7000
EXPOSE 119
CMD ["./streamnzb"]
