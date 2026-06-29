FROM scratch
ARG TARGETPLATFORM
LABEL org.opencontainers.image.source="https://github.com/hsn723/postfix_exporter" \
        org.opencontainers.image.authors="Hsn723" \
        org.opencontainers.image.title="postfix_exporter"
EXPOSE 9154
COPY $TARGETPLATFORM/postfix_exporter /
COPY LICENSE /
ENTRYPOINT ["/postfix_exporter"]
