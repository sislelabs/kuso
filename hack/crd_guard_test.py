#!/usr/bin/env python3
"""Fixture tests for hack/crd-guard.py. Run: python3 hack/crd_guard_test.py"""
import copy
import os
import subprocess
import sys
import tempfile
import unittest

import yaml

GUARD = os.environ.get("CRD_GUARD") or os.path.join(os.path.dirname(os.path.abspath(__file__)), "crd-guard.py")

BASE = {
    "apiVersion": "apiextensions.k8s.io/v1",
    "kind": "CustomResourceDefinition",
    "metadata": {"name": "widgets.example.com"},
    "spec": {
        "group": "example.com",
        "scope": "Namespaced",
        "names": {"plural": "widgets", "kind": "Widget"},
        "versions": [{
            "name": "v1",
            "served": True,
            "storage": True,
            "schema": {"openAPIV3Schema": {
                "type": "object",
                "properties": {"spec": {
                    "type": "object",
                    "x-kubernetes-preserve-unknown-fields": True,
                    "properties": {
                        "name": {"type": "string", "maxLength": 63},
                        "count": {"type": "integer", "minimum": 0},
                        "tags": {"type": "array", "items": {"type": "string"}},
                        "free": {"x-kubernetes-preserve-unknown-fields": True},
                        "port": {"x-kubernetes-int-or-string": True},
                        "labels": {"type": "object", "x-kubernetes-preserve-unknown-fields": True},
                    },
                }},
            }},
        }],
    },
}


def spec(doc):
    return doc["spec"]["versions"][0]["schema"]["openAPIV3Schema"]["properties"]["spec"]


def run_guard(old, new):
    with tempfile.TemporaryDirectory() as d:
        paths = []
        for i, doc in enumerate((old, new)):
            p = os.path.join(d, f"{i}.yaml")
            with open(p, "w") as fh:
                yaml.safe_dump(doc, fh)
            paths.append(p)
        res = subprocess.run([sys.executable, GUARD, *paths], capture_output=True, text=True)
        return res.returncode, res.stderr


class CRDGuardTest(unittest.TestCase):
    def assertBreaking(self, mutate, needle):
        new = copy.deepcopy(BASE)
        mutate(new)
        rc, err = run_guard(BASE, new)
        self.assertEqual(rc, 1, f"expected BREAKING for {needle!r}, got rc={rc}")
        self.assertIn(needle, err)

    def assertAdditive(self, mutate):
        new = copy.deepcopy(BASE)
        mutate(new)
        rc, err = run_guard(BASE, new)
        self.assertEqual(rc, 0, err)

    def test_unchanged(self):
        self.assertAdditive(lambda d: None)

    def test_new_optional_field_is_additive(self):
        self.assertAdditive(lambda d: spec(d)["properties"].update(extra={"type": "string", "maxLength": 5}))

    def test_loosened_bounds_are_additive(self):
        def loosen(d):
            spec(d)["properties"]["name"]["maxLength"] = 253
            spec(d)["properties"]["count"].pop("minimum")
        self.assertAdditive(loosen)

    def test_field_removed(self):
        self.assertBreaking(lambda d: spec(d)["properties"].pop("count"), "field removed")

    def test_preserve_unknown_fields_dropped(self):
        self.assertBreaking(lambda d: spec(d).pop("x-kubernetes-preserve-unknown-fields"),
                            "x-kubernetes-preserve-unknown-fields removed")

    def test_preserve_unknown_fields_set_false(self):
        self.assertBreaking(lambda d: spec(d).update({"x-kubernetes-preserve-unknown-fields": False}),
                            "x-kubernetes-preserve-unknown-fields removed")

    def test_max_length_decreased(self):
        self.assertBreaking(lambda d: spec(d)["properties"]["name"].update(maxLength=32), "maxLength tightened")

    def test_max_length_added(self):
        self.assertBreaking(lambda d: spec(d)["properties"]["tags"]["items"].update(maxLength=10),
                            "maxLength tightened")

    def test_pattern_added(self):
        self.assertBreaking(lambda d: spec(d)["properties"]["name"].update(pattern="^[a-z]+$"), "pattern")

    def test_maximum_added(self):
        self.assertBreaking(lambda d: spec(d)["properties"]["count"].update(maximum=10), "maximum tightened")

    def test_minimum_raised(self):
        self.assertBreaking(lambda d: spec(d)["properties"]["count"].update(minimum=1), "minimum tightened")

    def test_max_items_added(self):
        self.assertBreaking(lambda d: spec(d)["properties"]["tags"].update(maxItems=3), "maxItems tightened")

    def test_format_added(self):
        self.assertBreaking(lambda d: spec(d)["properties"]["name"].update(format="hostname"), "format")

    def test_cel_rule_added(self):
        self.assertBreaking(
            lambda d: spec(d).update({"x-kubernetes-validations": [{"rule": "self.count < 5"}]}),
            "x-kubernetes-validations")

    def test_new_required(self):
        self.assertBreaking(lambda d: spec(d).update(required=["name"]), "newly required")

    def test_type_changed(self):
        self.assertBreaking(lambda d: spec(d)["properties"]["count"].update(type="string"), "type changed")

    def test_type_added_to_untyped_node(self):
        self.assertBreaking(lambda d: spec(d)["properties"]["free"].update(type="string"), "previously untyped")

    def test_int_or_string_removed(self):
        def mutate(d):
            spec(d)["properties"]["port"] = {"type": "integer"}
        self.assertBreaking(mutate, "x-kubernetes-int-or-string removed")

    def test_additional_properties_added(self):
        self.assertBreaking(
            lambda d: spec(d)["properties"]["labels"].update(additionalProperties={"type": "string"}),
            "additionalProperties schema added")

    def test_additional_properties_narrowed(self):
        new = copy.deepcopy(BASE)
        spec(new)["properties"]["labels"].update(additionalProperties={"type": "string"})
        newer = copy.deepcopy(new)
        spec(newer)["properties"]["labels"]["additionalProperties"]["maxLength"] = 3
        rc, err = run_guard(new, newer)
        self.assertEqual(rc, 1)
        self.assertIn("maxLength tightened", err)

    def test_list_type_set_added(self):
        self.assertBreaking(lambda d: spec(d)["properties"]["tags"].update({"x-kubernetes-list-type": "set"}),
                            "x-kubernetes-list-type")

    def test_list_type_atomic_is_additive(self):
        self.assertAdditive(lambda d: spec(d)["properties"]["tags"].update({"x-kubernetes-list-type": "atomic"}))

    def test_one_of_added(self):
        self.assertBreaking(
            lambda d: spec(d).update(oneOf=[{"required": ["name"]}, {"required": ["count"]}]), "oneOf")

    def test_pattern_alternative_appended_is_additive(self):
        old = copy.deepcopy(BASE)
        spec(old)["properties"]["name"]["pattern"] = "^[a-z]+-conn$"
        new = copy.deepcopy(old)
        spec(new)["properties"]["name"]["pattern"] = "^[a-z]+-conn$|^[a-z]+-shared$"
        rc, err = run_guard(old, new)
        self.assertEqual(rc, 0, err)

    def test_pattern_rewritten_is_breaking(self):
        old = copy.deepcopy(BASE)
        spec(old)["properties"]["name"]["pattern"] = "^[a-z]+-conn$"
        new = copy.deepcopy(old)
        spec(new)["properties"]["name"]["pattern"] = "^[a-z]+-(conn|shared)$"
        rc, err = run_guard(old, new)
        self.assertEqual(rc, 1)
        self.assertIn("pattern", err)

    def test_pattern_after_escaping_backslash_is_breaking(self):
        old = copy.deepcopy(BASE)
        spec(old)["properties"]["name"]["pattern"] = "^a\\"
        new = copy.deepcopy(old)
        spec(new)["properties"]["name"]["pattern"] = "^a\\|b"
        rc, _ = run_guard(old, new)
        self.assertEqual(rc, 1)

    def test_cel_rule_or_widened_is_additive(self):
        old = copy.deepcopy(BASE)
        spec(old)["x-kubernetes-validations"] = [{"rule": "!self.name.startsWith('kuso-')"}]
        new = copy.deepcopy(old)
        spec(new)["x-kubernetes-validations"] = [
            {"rule": "self.name == 'kuso-x' || (!self.name.startsWith('kuso-'))"}]
        rc, err = run_guard(old, new)
        self.assertEqual(rc, 0, err)

    def test_cel_rule_unparenthesised_or_is_breaking(self):
        old = copy.deepcopy(BASE)
        spec(old)["x-kubernetes-validations"] = [{"rule": "a ? b : c"}]
        new = copy.deepcopy(old)
        spec(new)["x-kubernetes-validations"] = [{"rule": "x || a ? b : c"}]
        rc, err = run_guard(old, new)
        self.assertEqual(rc, 1)


if __name__ == "__main__":
    unittest.main()
