# Test-only custom node for the dev VM: a prompt that takes a known time.
import time


class Sleep:
    @classmethod
    def INPUT_TYPES(cls):
        return {"required": {"seconds": ("FLOAT", {"default": 1.0, "min": 0.0, "max": 600.0}),
                             "seed": ("INT", {"default": 0})}}

    RETURN_TYPES = ()
    FUNCTION = "run"
    OUTPUT_NODE = True
    CATEGORY = "test"

    def run(self, seconds, seed):
        time.sleep(seconds)
        return {}


NODE_CLASS_MAPPINGS = {"NkgSleep": Sleep}
