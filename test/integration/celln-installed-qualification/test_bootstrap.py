"""Offline bootstrap safety tests, never installed evidence."""
import importlib.util
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("bootstrap", Path(__file__).with_name("bootstrap-kind.py"))
assert spec is not None and spec.loader is not None
bootstrap = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bootstrap)


class BootstrapSafety(unittest.TestCase):
    def test_requires_owned_name(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "new"
            image = "kindest/node:v1.35.0@sha256:" + "a" * 64
            for name in ["celln-tenancy", "substrate-local", "kind", "hermes-celln-", "hermes-celln-x;true"]:
                with self.subTest(name=name), self.assertRaises(ValueError):
                    bootstrap.validate(name, path, image)
            bootstrap.validate("hermes-celln-unit-test", path, image)
            self.assertFalse(path.exists(), "validation must not create state")

    def test_refuses_existing_or_relative_state(self):
        image = "kindest/node:v1.35.0@sha256:" + "a" * 64
        with tempfile.TemporaryDirectory() as tmp:
            for path in [Path(tmp), Path("relative-new")]:
                with self.subTest(path=path), self.assertRaises(ValueError):
                    bootstrap.validate("hermes-celln-unit", path, image)

    def test_requires_version_tag_and_full_digest(self):
        with tempfile.TemporaryDirectory() as tmp:
            for image in ["kindest/node:v1.35.0", "kindest/node@sha256:"+"a"*64, "kindest/node:latest@sha256:"+"a"*64, "kindest/node:v1.35.0@sha256:"+"a"*63]:
                with self.subTest(image=image), self.assertRaises(ValueError):
                    bootstrap.validate("hermes-celln-unit", Path(tmp)/"new", image)


if __name__ == "__main__":
    unittest.main()
