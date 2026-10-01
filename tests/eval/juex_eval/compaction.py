"""Run real model compaction and persisted Runtime restart validation."""
from . import helper

def add_args(parser): helper.add_live_args(parser)
def run(args): return helper.run_live(args, 'compaction')
