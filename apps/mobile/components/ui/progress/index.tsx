"use client";

import { createProgress } from "@gluestack-ui/core/progress/creator";
import { View } from "react-native";

// Use the installed v5 core. Color/size classes are supplied by the product layer;
// core owns value context, accessible range and filled-track width calculation.
const UIProgress = createProgress({ Root: View, FilledTrack: View });

export const Progress = UIProgress;
export const ProgressFilledTrack = UIProgress.FilledTrack;
