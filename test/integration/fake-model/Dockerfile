# Built by test/integration/deploy-fake-model.sh from a statically linked
# binary; the build context is a temporary directory, never the repository.
FROM scratch
COPY fake-model /fake-model
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/fake-model", "-listen", ":8080"]
